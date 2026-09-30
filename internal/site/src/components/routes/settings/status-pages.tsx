import { t } from "@lingui/core/macro"
import { Plural, Trans } from "@lingui/react/macro"
import {
	AlertTriangleIcon,
	ArrowDownIcon,
	ArrowUpIcon,
	CopyIcon,
	ExternalLinkIcon,
	MoreHorizontalIcon,
	PenSquareIcon,
	PlusIcon,
	Trash2Icon,
	XIcon,
} from "lucide-react"
import { useStore } from "@nanostores/react"
import { type ReactNode, useMemo, useState } from "react"
import { getErrorMessage } from "@/components/network-monitors-table/monitor-form-utils"
import { prependBasePath } from "@/components/router"
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
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog"
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/use-toast"
import { isReadOnlyUser, pb } from "@/lib/api"
import { $systems } from "@/lib/stores"
import { getMonitorName, getMonitorTarget } from "@/lib/network-monitor-utils"
import { useNetworkMonitors } from "@/lib/use-network-monitors"
import { useOwnedRecords } from "@/lib/use-owned-records"
import { cn, copyToClipboard } from "@/lib/utils"
import type { NetworkMonitorRecord, StatusPageRecord, SystemRecord } from "@/types"

/** Same pattern as the status_pages.slug field. */
const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}$/
const MAX_MONITORS = 100
const MAX_SYSTEMS = 100

/** Suggest a slug from a title, e.g. "My Services!" -> "my-services". */
export function slugify(title: string) {
	return title
		.normalize("NFKD")
		.replace(/[̀-ͯ]/g, "")
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+/, "")
		.slice(0, 63)
		.replace(/-+$/, "")
}

export function getStatusPagePath(slug: string) {
	return prependBasePath(`/status/${slug}`)
}

function getStatusPageUrl(slug: string) {
	return `${window.location.origin}${getStatusPagePath(slug)}`
}

export default function StatusPagesSettings() {
	const { records } = useOwnedRecords<StatusPageRecord>("status_pages", "title")
	const { monitors } = useNetworkMonitors({})
	const systems = useStore($systems)
	const readOnly = isReadOnlyUser()
	const [dialogOpen, setDialogOpen] = useState(false)
	const [dialogKey, setDialogKey] = useState(0)
	const [editingRecord, setEditingRecord] = useState<StatusPageRecord | null>(null)
	const [deleteRecord, setDeleteRecord] = useState<StatusPageRecord | null>(null)

	const openDialog = (record: StatusPageRecord | null) => {
		setEditingRecord(record)
		setDialogKey((key) => key + 1)
		setDialogOpen(true)
	}

	const handleDelete = async (record: StatusPageRecord) => {
		try {
			await pb.collection("status_pages").delete(record.id)
		} catch (e) {
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(e) })
		}
	}

	return (
		<>
			<div className="grid grid-cols-1 sm:flex items-center justify-between gap-4 mb-3">
				<div>
					<h3 className="mb-1 text-lg font-medium">
						<Trans>Status Pages</Trans>
					</h3>
					<p className="text-sm text-muted-foreground leading-relaxed">
						<Trans>
							Share the status and uptime of selected systems and monitors on a page that can be viewed without logging
							in.
						</Trans>
					</p>
				</div>
				{!readOnly && (
					<Button variant="outline" className="h-10 shrink-0" onClick={() => openDialog(null)}>
						<PlusIcon className="size-4" />
						<span className="ms-1">
							<Trans>Add status page</Trans>
						</span>
					</Button>
				)}
			</div>
			{records.length === 0 ? (
				<p className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
					<Trans>No status pages.</Trans>
				</p>
			) : (
				<div className="rounded-md border overflow-x-auto">
					<Table>
						<TableHeader>
							<TableRow className="border-border/50">
								<TableHead className="px-4">
									<Trans>Title</Trans>
								</TableHead>
								<TableHead className="px-4">
									<Trans>URL</Trans>
								</TableHead>
								<TableHead className="px-4">
									<Trans>Components</Trans>
								</TableHead>
								<TableHead className="px-4 text-right sr-only">
									<Trans>Actions</Trans>
								</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{records.map((record) => (
								<TableRow key={record.id}>
									<TableCell className="px-4 py-3 max-w-60">
										<div className="flex items-center gap-2 min-w-0">
											<span className="font-medium truncate">{record.title}</span>
											{record.public ? (
												<Badge className="shrink-0 bg-green-500/15! text-green-700 dark:text-green-400">
													<Trans>Public</Trans>
												</Badge>
											) : (
												<Badge className="shrink-0 bg-muted! text-muted-foreground">
													<Trans>Private</Trans>
												</Badge>
											)}
										</div>
									</TableCell>
									<TableCell className="px-4 py-3 whitespace-nowrap">
										<div className="flex items-center gap-1">
											<a
												href={getStatusPagePath(record.slug)}
												target="_blank"
												rel="noopener"
												className="text-sm hover:underline font-mono"
											>
												/status/{record.slug}
											</a>
											<Button
												variant="ghost"
												size="icon"
												className="size-7"
												title={t`Copy URL`}
												onClick={() => copyToClipboard(getStatusPageUrl(record.slug))}
											>
												<CopyIcon className="size-3.5" />
												<span className="sr-only">
													<Trans>Copy URL</Trans>
												</span>
											</Button>
										</div>
									</TableCell>
									<TableCell className="px-4 py-3 whitespace-nowrap">
										<div className="grid">
											{(record.systems?.length ?? 0) > 0 && (
												<span>
													<Plural value={record.systems.length} one="# system" other="# systems" />
												</span>
											)}
											<span>
												<Plural value={record.monitors.length} one="# monitor" other="# monitors" />
											</span>
										</div>
									</TableCell>
									<TableCell className="px-4 py-3 text-right">
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
												<DropdownMenuItem asChild>
													<a href={getStatusPagePath(record.slug)} target="_blank" rel="noopener">
														<ExternalLinkIcon className="me-2.5 size-4" />
														<Trans>Open</Trans>
													</a>
												</DropdownMenuItem>
												<DropdownMenuItem onClick={() => copyToClipboard(getStatusPageUrl(record.slug))}>
													<CopyIcon className="me-2.5 size-4" />
													<Trans>Copy URL</Trans>
												</DropdownMenuItem>
												{!readOnly && (
													<>
														<DropdownMenuItem onClick={() => openDialog(record)}>
															<PenSquareIcon className="me-2.5 size-4" />
															<Trans>Edit</Trans>
														</DropdownMenuItem>
														<DropdownMenuSeparator />
														<DropdownMenuItem onClick={() => setDeleteRecord(record)}>
															<Trash2Icon className="me-2.5 size-4" />
															<Trans>Delete</Trans>
														</DropdownMenuItem>
													</>
												)}
											</DropdownMenuContent>
										</DropdownMenu>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				</div>
			)}
			{!readOnly && (
				<Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
					<StatusPageDialog
						key={dialogKey}
						record={editingRecord}
						monitors={monitors}
						systems={systems}
						onClose={() => setDialogOpen(false)}
					/>
				</Dialog>
			)}
			<AlertDialog open={!!deleteRecord} onOpenChange={(open) => !open && setDeleteRecord(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							<Trans>Are you sure?</Trans>
						</AlertDialogTitle>
						<AlertDialogDescription>
							<Trans>
								This will permanently delete the status page "{deleteRecord?.title}". Its link will stop working.
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

function SwitchRow({
	id,
	label,
	description,
	checked,
	onCheckedChange,
}: {
	id: string
	label: React.ReactNode
	description?: React.ReactNode
	checked: boolean
	onCheckedChange: (checked: boolean) => void
}) {
	return (
		<div className="flex items-center justify-between gap-4">
			<Label htmlFor={id} className="grid gap-1 font-normal">
				<span className="font-medium">{label}</span>
				{description && <span className="text-xs text-muted-foreground">{description}</span>}
			</Label>
			<Switch id={id} checked={checked} onCheckedChange={onCheckedChange} />
		</div>
	)
}

function StatusPageDialog({
	record,
	monitors,
	systems,
	onClose,
}: {
	record: StatusPageRecord | null
	monitors: NetworkMonitorRecord[]
	systems: SystemRecord[]
	onClose: () => void
}) {
	const [title, setTitle] = useState(record?.title ?? "")
	const [slug, setSlug] = useState(record?.slug ?? "")
	// suggest the slug from the title until the user edits it
	const [slugEdited, setSlugEdited] = useState(!!record)
	const [description, setDescription] = useState(record?.description ?? "")
	const [isPublic, setIsPublic] = useState(record?.public ?? true)
	const [showTargets, setShowTargets] = useState(record?.showTargets ?? false)
	const [showResponseTimes, setShowResponseTimes] = useState(record?.showResponseTimes ?? false)
	const [autoIncidents, setAutoIncidents] = useState(record?.autoIncidents ?? false)
	const [selected, setSelected] = useState<string[]>(record?.monitors ?? [])
	const [selectedSystems, setSelectedSystems] = useState<string[]>(record?.systems ?? [])
	const [saving, setSaving] = useState(false)

	const monitorItems = useMemo(
		() =>
			monitors
				.map((m) => {
					const target = getMonitorTarget(m)
					return { id: m.id, name: getMonitorName(m), detail: m.name && target ? target : undefined }
				})
				.sort((a, b) => a.name.localeCompare(b.name)),
		[monitors]
	)
	const systemItems = useMemo(
		() => systems.map((s) => ({ id: s.id, name: s.name, detail: s.host })).sort((a, b) => a.name.localeCompare(b.name)),
		[systems]
	)
	const slugValid = SLUG_PATTERN.test(slug)

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault()
		if (!slugValid) return
		const data = {
			user: pb.authStore.record?.id,
			title: title.trim(),
			slug,
			description: description.trim(),
			public: isPublic,
			showTargets,
			showResponseTimes,
			autoIncidents,
			monitors: selected,
			systems: selectedSystems,
		}
		setSaving(true)
		try {
			if (record) {
				await pb.collection("status_pages").update(record.id, data)
			} else {
				await pb.collection("status_pages").create(data)
			}
			onClose()
		} catch (err) {
			const slugError = (err as { response?: { data?: { slug?: { code?: string } } } })?.response?.data?.slug
			toast({
				variant: "destructive",
				title: t`Failed to save status page`,
				description:
					slugError?.code === "validation_not_unique" ? t`This slug is already in use.` : getErrorMessage(err),
			})
		} finally {
			setSaving(false)
		}
	}

	return (
		<DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
			<DialogHeader>
				<DialogTitle>{record ? <Trans>Edit status page</Trans> : <Trans>Add status page</Trans>}</DialogTitle>
				<DialogDescription>
					<Trans>Choose which systems and monitors to show and what details are visible.</Trans>
				</DialogDescription>
			</DialogHeader>
			<form onSubmit={handleSubmit} className="grid gap-4 min-w-0">
				<div className="grid gap-2">
					<Label htmlFor="sp-title">
						<Trans>Title</Trans>
					</Label>
					<Input
						id="sp-title"
						value={title}
						required
						maxLength={200}
						onChange={(e) => {
							setTitle(e.target.value)
							if (!slugEdited) setSlug(slugify(e.target.value))
						}}
					/>
				</div>
				<div className="grid gap-2">
					<Label htmlFor="sp-slug">
						<Trans>Slug</Trans>
					</Label>
					<div className="flex items-center rounded-md border border-input focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2 focus-within:ring-offset-background">
						<span className="ps-3 text-sm text-muted-foreground whitespace-nowrap">/status/</span>
						<Input
							id="sp-slug"
							value={slug}
							required
							maxLength={63}
							aria-invalid={!!slug && !slugValid}
							className="border-0 ps-0.5 focus-visible:ring-0 focus-visible:ring-offset-0 font-mono"
							onChange={(e) => {
								setSlugEdited(true)
								setSlug(e.target.value.toLowerCase())
							}}
						/>
					</div>
					{slug && !slugValid ? (
						<p className="text-xs text-destructive">
							<Trans>Use 2-63 lowercase letters, numbers and hyphens, starting with a letter or number.</Trans>
						</p>
					) : (
						<p className="text-xs text-muted-foreground break-all">{getStatusPageUrl(slug || "…")}</p>
					)}
				</div>
				<div className="grid gap-2">
					<Label htmlFor="sp-description">
						<Trans>Description</Trans>
					</Label>
					<Textarea
						id="sp-description"
						value={description}
						onChange={(e) => setDescription(e.target.value)}
						rows={2}
						maxLength={2000}
					/>
				</div>
				<SwitchRow
					id="sp-public"
					label={<Trans>Public</Trans>}
					description={<Trans>Anyone with the link can view the page. Otherwise only you can.</Trans>}
					checked={isPublic}
					onCheckedChange={setIsPublic}
				/>
				<SwitchRow
					id="sp-targets"
					label={<Trans>Show targets</Trans>}
					description={
						showTargets ? (
							<span className="flex items-start gap-1.5 text-amber-600 dark:text-amber-400">
								<AlertTriangleIcon className="size-3.5 shrink-0 mt-px" />
								<Trans>Targets (URLs, hostnames, IPs) will be visible to anyone with the link</Trans>
							</span>
						) : (
							<Trans>Show each monitor's URL, hostname or IP.</Trans>
						)
					}
					checked={showTargets}
					onCheckedChange={setShowTargets}
				/>
				<SwitchRow
					id="sp-response"
					label={<Trans>Show response times</Trans>}
					checked={showResponseTimes}
					onCheckedChange={setShowResponseTimes}
				/>
				<SwitchRow
					id="sp-auto-incidents"
					label={<Trans>Automatically create incidents</Trans>}
					description={
						<Trans>
							Open an incident when a system or monitor on this page goes down, and resolve it when it recovers.
						</Trans>
					}
					checked={autoIncidents}
					onCheckedChange={setAutoIncidents}
				/>
				<OrderedPicker
					id="sp-add-system"
					label={<Trans>Systems</Trans>}
					description={<Trans>Only the system name and status history are shown.</Trans>}
					items={systemItems}
					selected={selectedSystems}
					onChange={setSelectedSystems}
					max={MAX_SYSTEMS}
					addPlaceholder={t`Add system…`}
					emptyPlaceholder={t`No more systems to add`}
					unavailableLabel={<Trans>Unavailable system</Trans>}
					maxMessage={<Trans>A status page can show up to {MAX_SYSTEMS} systems.</Trans>}
				/>
				<OrderedPicker
					id="sp-add-monitor"
					label={<Trans>Monitors</Trans>}
					items={monitorItems}
					selected={selected}
					onChange={setSelected}
					max={MAX_MONITORS}
					addPlaceholder={t`Add monitor…`}
					emptyPlaceholder={t`No more monitors to add`}
					unavailableLabel={<Trans>Unavailable monitor</Trans>}
					maxMessage={<Trans>A status page can show up to {MAX_MONITORS} monitors.</Trans>}
				/>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						<Trans>Cancel</Trans>
					</Button>
					<Button type="submit" disabled={saving || !slugValid}>
						{record ? <Trans>Update</Trans> : <Trans>Create</Trans>}
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}

interface PickerItem {
	id: string
	name: string
	/** Secondary text shown next to the name in the list. */
	detail?: string
}

/** An ordered list of selected items with controls to reorder, remove and add items. */
function OrderedPicker({
	id,
	label,
	description,
	items,
	selected,
	onChange,
	max,
	addPlaceholder,
	emptyPlaceholder,
	unavailableLabel,
	maxMessage,
}: {
	id: string
	label: ReactNode
	description?: ReactNode
	/** All items that can be selected, in display order. */
	items: PickerItem[]
	selected: string[]
	onChange: (selected: string[]) => void
	max: number
	addPlaceholder: string
	emptyPlaceholder: string
	unavailableLabel: ReactNode
	maxMessage: ReactNode
}) {
	const itemsById = useMemo(() => new Map(items.map((item) => [item.id, item])), [items])
	const available = useMemo(() => items.filter((item) => !selected.includes(item.id)), [items, selected])

	const move = (index: number, delta: number) => {
		const next = [...selected]
		const [item] = next.splice(index, 1)
		next.splice(index + delta, 0, item)
		onChange(next)
	}

	return (
		<div className="grid gap-2 min-w-0">
			<Label htmlFor={id}>{label}</Label>
			{description && <p className="-mt-1 text-xs text-muted-foreground">{description}</p>}
			{selected.length > 0 && (
				<ol className="rounded-md border divide-y">
					{selected.map((itemId, index) => {
						const item = itemsById.get(itemId)
						return (
							<li key={itemId} className="flex items-center gap-2 ps-3 pe-1 py-1 min-w-0">
								<span className="w-5 shrink-0 text-xs text-muted-foreground tabular-nums">{index + 1}</span>
								<span className="min-w-0 flex-1 truncate text-sm">
									{item ? item.name : unavailableLabel}
									{item?.detail && <span className="ms-2 text-xs text-muted-foreground">{item.detail}</span>}
								</span>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									className="size-7 shrink-0"
									disabled={index === 0}
									onClick={() => move(index, -1)}
									title={t`Move up`}
								>
									<ArrowUpIcon className="size-3.5" />
									<span className="sr-only">
										<Trans>Move up</Trans>
									</span>
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									className="size-7 shrink-0"
									disabled={index === selected.length - 1}
									onClick={() => move(index, 1)}
									title={t`Move down`}
								>
									<ArrowDownIcon className="size-3.5" />
									<span className="sr-only">
										<Trans>Move down</Trans>
									</span>
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									className="size-7 shrink-0"
									onClick={() => onChange(selected.filter((s) => s !== itemId))}
									title={t`Remove`}
								>
									<XIcon className="size-3.5" />
									<span className="sr-only">
										<Trans>Remove</Trans>
									</span>
								</Button>
							</li>
						)
					})}
				</ol>
			)}
			<Select
				value=""
				onValueChange={(itemId) => itemId && onChange([...selected, itemId])}
				disabled={available.length === 0 || selected.length >= max}
			>
				<SelectTrigger id={id}>
					<SelectValue placeholder={available.length === 0 ? emptyPlaceholder : addPlaceholder} />
				</SelectTrigger>
				<SelectContent>
					{available.map((item) => (
						<SelectItem key={item.id} value={item.id}>
							{item.name}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			{selected.length >= max && <p className="text-xs text-muted-foreground">{maxMessage}</p>}
		</div>
	)
}
