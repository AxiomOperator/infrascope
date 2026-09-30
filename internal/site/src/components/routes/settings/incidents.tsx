import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import {
	CheckCircle2Icon,
	ChevronDownIcon,
	ChevronRightIcon,
	MessageSquarePlusIcon,
	MoreHorizontalIcon,
	PenSquareIcon,
	PlusIcon,
	Trash2Icon,
	ZapIcon,
} from "lucide-react"
import { useEffect, useMemo, useState } from "react"
import { type ChecklistItem, IncidentDialog, IncidentUpdateDialog } from "@/components/incidents/incident-dialog"
import {
	IncidentImpactBadge,
	IncidentStatusBadge,
	IncidentTimeline,
	incidentImpactCardColors,
} from "@/components/incidents/incident-ui"
import { getErrorMessage } from "@/components/network-monitors-table/monitor-form-utils"
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Dialog } from "@/components/ui/dialog"
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "@/components/ui/use-toast"
import { isReadOnlyUser, pb } from "@/lib/api"
import { formatIncidentDuration, splitIncidents } from "@/lib/incidents"
import { formatRelativeTime, getMonitorName, getMonitorTarget } from "@/lib/network-monitor-utils"
import { $systems } from "@/lib/stores"
import { useNetworkMonitors } from "@/lib/use-network-monitors"
import { useOwnedRecords } from "@/lib/use-owned-records"
import { cn, formatShortDate } from "@/lib/utils"
import type { IncidentRecord, IncidentStatus, IncidentUpdateRecord, StatusPageRecord } from "@/types"

type DialogState =
	| { kind: "edit"; record: IncidentRecord | null }
	| { kind: "update"; record: IncidentRecord; status: IncidentStatus }
	| null

export default function IncidentsSettings() {
	const { records } = useOwnedRecords<IncidentRecord>("incidents", "-startedAt")
	const { records: updates } = useOwnedRecords<IncidentUpdateRecord>("incident_updates", "-created")
	const { records: pages } = useOwnedRecords<StatusPageRecord>("status_pages", "title")
	const { monitors } = useNetworkMonitors({})
	const systems = useStore($systems)
	const readOnly = isReadOnlyUser()
	const [dialog, setDialog] = useState<DialogState>(null)
	const [dialogKey, setDialogKey] = useState(0)
	const [deleteRecord, setDeleteRecord] = useState<IncidentRecord | null>(null)
	const [now, setNow] = useState(() => Date.now())

	useEffect(() => {
		const interval = setInterval(() => setNow(Date.now()), 60_000)
		return () => clearInterval(interval)
	}, [])

	const { active, resolved } = useMemo(() => splitIncidents(records), [records])
	const updatesByIncident = useMemo(() => {
		const map = new Map<string, IncidentUpdateRecord[]>()
		for (const update of updates) {
			const list = map.get(update.incident) ?? []
			list.push(update)
			map.set(update.incident, list)
		}
		for (const list of map.values()) list.sort((a, b) => b.created.localeCompare(a.created))
		return map
	}, [updates])
	const monitorItems = useMemo<ChecklistItem[]>(
		() =>
			monitors.map((m) => {
				const target = getMonitorTarget(m)
				return { id: m.id, name: getMonitorName(m), detail: m.name && target ? target : undefined }
			}),
		[monitors]
	)
	const systemItems = useMemo<ChecklistItem[]>(() => systems.map((s) => ({ id: s.id, name: s.name })), [systems])
	const pageItems = useMemo<ChecklistItem[]>(
		() => pages.map((p) => ({ id: p.id, name: p.title, detail: `/status/${p.slug}` })),
		[pages]
	)
	const names = useMemo(() => {
		const map = new Map<string, string>()
		for (const item of [...monitorItems, ...systemItems]) map.set(item.id, item.name)
		return map
	}, [monitorItems, systemItems])
	const pageNames = useMemo(() => new Map(pages.map((p) => [p.id, p.title])), [pages])

	const open = (next: DialogState) => {
		setDialog(next)
		setDialogKey((key) => key + 1)
	}

	const handleDelete = async (record: IncidentRecord) => {
		try {
			await pb.collection("incidents").delete(record.id)
		} catch (e) {
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(e) })
		}
	}

	const renderCard = (record: IncidentRecord, defaultExpanded: boolean) => (
		<IncidentCard
			key={record.id}
			record={record}
			updates={updatesByIncident.get(record.id) ?? []}
			components={[...record.monitors, ...record.systems].map((id) => names.get(id)).filter((n): n is string => !!n)}
			pages={record.statusPages.map((id) => pageNames.get(id)).filter((n): n is string => !!n)}
			now={now}
			readOnly={readOnly}
			defaultExpanded={defaultExpanded}
			onEdit={() => open({ kind: "edit", record })}
			onUpdate={(status) => open({ kind: "update", record, status })}
			onDelete={() => setDeleteRecord(record)}
		/>
	)

	return (
		<>
			<div className="grid grid-cols-1 sm:flex items-center justify-between gap-4 mb-3">
				<div>
					<h3 className="mb-1 text-lg font-medium">
						<Trans>Incidents</Trans>
					</h3>
					<p className="text-sm text-muted-foreground leading-relaxed">
						<Trans>Report outages on your status pages and keep visitors informed with a timeline of updates.</Trans>
					</p>
				</div>
				{!readOnly && (
					<Button variant="outline" className="h-10 shrink-0" onClick={() => open({ kind: "edit", record: null })}>
						<PlusIcon className="size-4" />
						<span className="ms-1">
							<Trans>Create incident</Trans>
						</span>
					</Button>
				)}
			</div>
			<div className="grid gap-6">
				<section className="grid gap-3">
					<h4 className="text-sm font-semibold text-muted-foreground">
						<Trans>Active</Trans>
					</h4>
					{active.length === 0 ? (
						<p className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
							<Trans>No active incidents.</Trans>
						</p>
					) : (
						active.map((record) => renderCard(record, true))
					)}
				</section>
				{resolved.length > 0 && (
					<section className="grid gap-3">
						<h4 className="text-sm font-semibold text-muted-foreground">
							<Trans>Resolved</Trans>
						</h4>
						{resolved.map((record) => renderCard(record, false))}
					</section>
				)}
			</div>
			{!readOnly && (
				<Dialog open={!!dialog} onOpenChange={(isOpen) => !isOpen && setDialog(null)}>
					{dialog?.kind === "edit" && (
						<IncidentDialog
							key={dialogKey}
							record={dialog.record}
							monitors={monitorItems}
							systems={systemItems}
							statusPages={pageItems}
							onClose={() => setDialog(null)}
						/>
					)}
					{dialog?.kind === "update" && (
						<IncidentUpdateDialog
							key={dialogKey}
							incident={dialog.record}
							initialStatus={dialog.status}
							onClose={() => setDialog(null)}
						/>
					)}
				</Dialog>
			)}
			<AlertDialog open={!!deleteRecord} onOpenChange={(isOpen) => !isOpen && setDeleteRecord(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							<Trans>Are you sure?</Trans>
						</AlertDialogTitle>
						<AlertDialogDescription>
							<Trans>
								This will permanently delete the incident "{deleteRecord?.title}" and its updates. Status pages stop
								showing it.
							</Trans>
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>
							<Trans>Cancel</Trans>
						</AlertDialogCancel>
						<AlertDialogAction
							className={cn(buttonVariants({ variant: "destructive" }))}
							onClick={() => deleteRecord && handleDelete(deleteRecord)}
						>
							<Trans>Delete</Trans>
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	)
}

function IncidentCard({
	record,
	updates,
	components,
	pages,
	now,
	readOnly,
	defaultExpanded,
	onEdit,
	onUpdate,
	onDelete,
}: {
	record: IncidentRecord
	updates: IncidentUpdateRecord[]
	components: string[]
	pages: string[]
	now: number
	readOnly: boolean
	defaultExpanded: boolean
	onEdit: () => void
	onUpdate: (status: IncidentStatus) => void
	onDelete: () => void
}) {
	const [expanded, setExpanded] = useState(defaultExpanded)
	const isResolved = record.status === "resolved"
	const started = record.startedAt || record.created
	const end = isResolved && record.resolvedAt ? new Date(record.resolvedAt).getTime() : now
	const duration = formatIncidentDuration(end - new Date(started).getTime())
	const timelineId = `incident-timeline-${record.id}`

	return (
		<div className={cn("rounded-lg border px-4 py-3", !isResolved && incidentImpactCardColors[record.impact])}>
			<div className="flex items-start gap-2">
				<button
					type="button"
					className="mt-0.5 shrink-0 text-muted-foreground hover:text-foreground"
					onClick={() => setExpanded((value) => !value)}
					aria-expanded={expanded}
					aria-controls={timelineId}
					title={expanded ? t`Hide updates` : t`Show updates`}
				>
					{expanded ? <ChevronDownIcon className="size-4" /> : <ChevronRightIcon className="size-4" />}
					<span className="sr-only">{expanded ? <Trans>Hide updates</Trans> : <Trans>Show updates</Trans>}</span>
				</button>
				<div className="min-w-0 flex-1 grid gap-1.5">
					<div className="flex flex-wrap items-center gap-2">
						<h5 className="font-semibold break-words min-w-0">{record.title}</h5>
						<IncidentStatusBadge status={record.status} />
						<IncidentImpactBadge impact={record.impact} />
						{record.auto && (
							<Badge variant="outline" className="gap-1" title={t`Created automatically by a status page`}>
								<ZapIcon className="size-3" />
								<Trans>Automatic</Trans>
							</Badge>
						)}
					</div>
					<p className="text-xs text-muted-foreground tabular-nums" title={formatShortDate(started)}>
						{isResolved ? (
							<Trans>
								Resolved {formatRelativeTime(record.resolvedAt, now)} after {duration}
							</Trans>
						) : (
							<Trans>
								Started {formatRelativeTime(started, now)} ({duration})
							</Trans>
						)}
					</p>
					{(components.length > 0 || pages.length > 0) && (
						<dl className="grid gap-0.5 text-xs text-muted-foreground">
							{components.length > 0 && (
								<div className="flex gap-1 min-w-0">
									<dt className="shrink-0">
										<Trans>Affects</Trans>
									</dt>
									<dd className="truncate text-foreground" title={components.join(", ")}>
										{components.join(", ")}
									</dd>
								</div>
							)}
							{pages.length > 0 && (
								<div className="flex gap-1 min-w-0">
									<dt className="shrink-0">
										<Trans>Shown on</Trans>
									</dt>
									<dd className="truncate text-foreground" title={pages.join(", ")}>
										{pages.join(", ")}
									</dd>
								</div>
							)}
						</dl>
					)}
				</div>
				{!readOnly && (
					<div className="flex shrink-0 items-center gap-1">
						{!isResolved && (
							<Button
								variant="outline"
								size="sm"
								className="h-8 hidden sm:inline-flex"
								onClick={() => onUpdate("resolved")}
							>
								<CheckCircle2Icon className="size-4" />
								<span className="ms-1">
									<Trans>Resolve</Trans>
								</span>
							</Button>
						)}
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button variant="ghost" size="icon" className="size-8">
									<span className="sr-only">
										<Trans>Open menu</Trans>
									</span>
									<MoreHorizontalIcon className="size-4" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem onClick={() => onUpdate(isResolved ? "monitoring" : record.status)}>
									<MessageSquarePlusIcon className="me-2.5 size-4" />
									<Trans>Post update</Trans>
								</DropdownMenuItem>
								{!isResolved && (
									<DropdownMenuItem onClick={() => onUpdate("resolved")}>
										<CheckCircle2Icon className="me-2.5 size-4" />
										<Trans>Resolve</Trans>
									</DropdownMenuItem>
								)}
								<DropdownMenuItem onClick={onEdit}>
									<PenSquareIcon className="me-2.5 size-4" />
									<Trans>Edit</Trans>
								</DropdownMenuItem>
								<DropdownMenuSeparator />
								<DropdownMenuItem onClick={onDelete}>
									<Trash2Icon className="me-2.5 size-4" />
									<Trans>Delete</Trans>
								</DropdownMenuItem>
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				)}
			</div>
			{expanded && (
				<div id={timelineId} className="mt-3 ms-6">
					{updates.length === 0 ? (
						<p className="text-sm text-muted-foreground">
							<Trans>No updates yet.</Trans>
						</p>
					) : (
						<IncidentTimeline updates={updates} />
					)}
				</div>
			)}
		</div>
	)
}
