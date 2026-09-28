import { t } from "@lingui/core/macro"
import { Plural, Trans } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { MoreHorizontalIcon, PenSquareIcon, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react"
import { useEffect, useMemo, useState } from "react"
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
import { Checkbox } from "@/components/ui/checkbox"
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
import { getMonitorName, getMonitorTarget } from "@/lib/network-monitor-utils"
import { $userSettings } from "@/lib/stores"
import { useNetworkMonitors } from "@/lib/use-network-monitors"
import { useOwnedRecords } from "@/lib/use-owned-records"
import { cn, formatShortDate, hourWithMinutes } from "@/lib/utils"
import type { MaintenanceRecord, NetworkMonitorRecord } from "@/types"

type WindowState = "active" | "upcoming" | "ended" | "inactive"

/**
 * State of a maintenance window. Daily windows store their times like quiet hours:
 * as full dates whose UTC time of day, shifted by the stored date's offset, is the local time.
 */
function getWindowState(record: MaintenanceRecord, now = new Date()): WindowState {
	const startDate = new Date(record.start)
	const endDate = new Date(record.end)
	if (record.type === "daily") {
		const currentMinutes = now.getHours() * 60 + now.getMinutes()
		const startMinutes = startDate.getUTCHours() * 60 + startDate.getUTCMinutes()
		const endMinutes = endDate.getUTCHours() * 60 + endDate.getUTCMinutes()
		// use the stored date's offset to avoid DST mismatches
		const localStart = (startMinutes - startDate.getTimezoneOffset() + 1440) % 1440
		const localEnd = (endMinutes - endDate.getTimezoneOffset() + 1440) % 1440
		const active =
			localStart <= localEnd
				? currentMinutes >= localStart && currentMinutes < localEnd
				: currentMinutes >= localStart || currentMinutes < localEnd
		return active ? "active" : "inactive"
	}
	if (now < startDate) return "upcoming"
	if (now < endDate) return "active"
	return "ended"
}

function formatSchedule(record: MaintenanceRecord) {
	if (record.type === "daily") {
		return `${hourWithMinutes(record.start)} – ${hourWithMinutes(record.end)}`
	}
	return `${formatShortDate(record.start)} – ${formatShortDate(record.end)}`
}

/** Format a Date as a datetime-local value (YYYY-MM-DDTHH:mm) in local time. */
function formatDateTimeLocal(date: Date): string {
	const pad = (n: number) => String(n).padStart(2, "0")
	return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function WindowStateBadge({ state }: { state: WindowState }) {
	switch (state) {
		case "active":
			return (
				<Badge className="bg-blue-500/15! text-blue-600 dark:text-blue-400">
					<Trans>Active now</Trans>
				</Badge>
			)
		case "upcoming":
			return (
				<Badge variant="outline">
					<Trans>Upcoming</Trans>
				</Badge>
			)
		case "ended":
			return (
				<Badge className="bg-muted! text-muted-foreground">
					<Trans>Ended</Trans>
				</Badge>
			)
		default:
			return (
				<Badge className="bg-muted! text-muted-foreground">
					<Trans>Scheduled</Trans>
				</Badge>
			)
	}
}

export default function MaintenanceSettings() {
	const { records } = useOwnedRecords<MaintenanceRecord>("monitor_maintenance", "-start")
	const { monitors } = useNetworkMonitors({})
	// re-render when the user changes the 12h / 24h setting
	useStore($userSettings, { keys: ["hourFormat"] })
	const readOnly = isReadOnlyUser()
	const [dialogOpen, setDialogOpen] = useState(false)
	const [dialogKey, setDialogKey] = useState(0)
	const [editingRecord, setEditingRecord] = useState<MaintenanceRecord | null>(null)
	const [deleteRecord, setDeleteRecord] = useState<MaintenanceRecord | null>(null)
	const [now, setNow] = useState(() => new Date())

	// refresh active state every minute
	useEffect(() => {
		const interval = setInterval(() => setNow(new Date()), 60_000)
		return () => clearInterval(interval)
	}, [])

	const monitorsById = useMemo(() => new Map(monitors.map((m) => [m.id, m])), [monitors])

	const openDialog = (record: MaintenanceRecord | null) => {
		setEditingRecord(record)
		setDialogKey((key) => key + 1)
		setDialogOpen(true)
	}

	const handleDelete = async (record: MaintenanceRecord) => {
		try {
			await pb.collection("monitor_maintenance").delete(record.id)
		} catch (e) {
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(e) })
		}
	}

	const monitorNames = (record: MaintenanceRecord) =>
		record.monitors
			.map((id) => monitorsById.get(id))
			.filter((m): m is NetworkMonitorRecord => !!m)
			.map(getMonitorName)

	return (
		<>
			<div className="grid grid-cols-1 sm:flex items-center justify-between gap-4 mb-3">
				<div>
					<h3 className="mb-1 text-lg font-medium">
						<Trans>Maintenance</Trans>
					</h3>
					<p className="text-sm text-muted-foreground leading-relaxed">
						<Trans>
							Schedule maintenance windows for monitors. Monitors show as under maintenance, downtime is not counted and
							no notifications are sent.
						</Trans>
					</p>
				</div>
				{!readOnly && (
					<Button variant="outline" className="h-10 shrink-0" onClick={() => openDialog(null)}>
						<PlusIcon className="size-4" />
						<span className="ms-1">
							<Trans>Add maintenance</Trans>
						</span>
					</Button>
				)}
			</div>
			{records.length === 0 ? (
				<p className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
					<Trans>No maintenance windows.</Trans>
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
									<Trans>Schedule</Trans>
								</TableHead>
								<TableHead className="px-4">
									<Trans>Monitors</Trans>
								</TableHead>
								<TableHead className="px-4">
									<Trans>State</Trans>
								</TableHead>
								{!readOnly && (
									<TableHead className="px-4 text-right sr-only">
										<Trans>Actions</Trans>
									</TableHead>
								)}
							</TableRow>
						</TableHeader>
						<TableBody>
							{records.map((record) => {
								const names = monitorNames(record)
								const count = record.monitors.length
								return (
									<TableRow key={record.id}>
										<TableCell className="px-4 py-3 max-w-60">
											<div className="font-medium truncate">{record.title}</div>
											{record.description && (
												<div className="text-xs text-muted-foreground truncate">{record.description}</div>
											)}
										</TableCell>
										<TableCell className="px-4 py-3 whitespace-nowrap">
											<div className="tabular-nums">{formatSchedule(record)}</div>
											<div className="text-xs text-muted-foreground">
												{record.type === "daily" ? <Trans>Daily</Trans> : <Trans>One-time</Trans>}
											</div>
										</TableCell>
										<TableCell className="px-4 py-3 max-w-60">
											<div className="whitespace-nowrap">
												<Plural value={count} one="# monitor" other="# monitors" />
											</div>
											{names.length > 0 && (
												<div className="text-xs text-muted-foreground truncate" title={names.join(", ")}>
													{names.join(", ")}
												</div>
											)}
										</TableCell>
										<TableCell className="px-4 py-3">
											<WindowStateBadge state={getWindowState(record, now)} />
										</TableCell>
										{!readOnly && (
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
														<DropdownMenuItem onClick={() => openDialog(record)}>
															<PenSquareIcon className="me-2.5 size-4" />
															<Trans>Edit</Trans>
														</DropdownMenuItem>
														<DropdownMenuSeparator />
														<DropdownMenuItem onClick={() => setDeleteRecord(record)}>
															<Trash2Icon className="me-2.5 size-4" />
															<Trans>Delete</Trans>
														</DropdownMenuItem>
													</DropdownMenuContent>
												</DropdownMenu>
											</TableCell>
										)}
									</TableRow>
								)
							})}
						</TableBody>
					</Table>
				</div>
			)}
			{!readOnly && (
				<Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
					<MaintenanceDialog
						key={dialogKey}
						record={editingRecord}
						monitors={monitors}
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
							<Trans>This will permanently delete the maintenance window "{deleteRecord?.title}".</Trans>
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

function MaintenanceDialog({
	record,
	monitors,
	onClose,
}: {
	record: MaintenanceRecord | null
	monitors: NetworkMonitorRecord[]
	onClose: () => void
}) {
	const [initial] = useState(() => {
		const noon = new Date()
		noon.setHours(12, 0, 0, 0)
		const onePm = new Date(noon)
		onePm.setHours(13)
		const start = record ? new Date(record.start) : noon
		const end = record ? new Date(record.end) : onePm
		return {
			startDateTime: formatDateTimeLocal(start),
			endDateTime: formatDateTimeLocal(end),
			startTime: start.toTimeString().slice(0, 5),
			endTime: end.toTimeString().slice(0, 5),
		}
	})
	const [title, setTitle] = useState(record?.title ?? "")
	const [description, setDescription] = useState(record?.description ?? "")
	const [windowType, setWindowType] = useState<MaintenanceRecord["type"]>(record?.type ?? "one-time")
	const [startDateTime, setStartDateTime] = useState(initial.startDateTime)
	const [endDateTime, setEndDateTime] = useState(initial.endDateTime)
	const [startTime, setStartTime] = useState(initial.startTime)
	const [endTime, setEndTime] = useState(initial.endTime)
	const [selected, setSelected] = useState<string[]>(record?.monitors ?? [])
	const [showOnStatusPages, setShowOnStatusPages] = useState(record?.showOnStatusPages ?? true)
	const [saving, setSaving] = useState(false)

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault()
		let start: string
		let end: string
		if (windowType === "daily") {
			if (startTime === endTime) {
				toast({ variant: "destructive", title: t`Error`, description: t`Start and end time must differ.` })
				return
			}
			// same convention as quiet hours: today's date so the current DST offset is applied
			const today = new Date().toISOString().split("T")[0]
			start = new Date(`${today}T${startTime}:00`).toISOString()
			end = new Date(`${today}T${endTime}:00`).toISOString()
		} else {
			const startDate = new Date(startDateTime)
			const endDate = new Date(endDateTime)
			if (!(endDate > startDate)) {
				toast({ variant: "destructive", title: t`Error`, description: t`End time must be after start time.` })
				return
			}
			start = startDate.toISOString()
			end = endDate.toISOString()
		}
		if (selected.length === 0) {
			toast({ variant: "destructive", title: t`Error`, description: t`Select at least one monitor.` })
			return
		}
		const data = {
			user: pb.authStore.record?.id,
			title: title.trim(),
			description: description.trim(),
			type: windowType,
			start,
			end,
			monitors: selected,
			showOnStatusPages,
		}
		setSaving(true)
		try {
			if (record) {
				await pb.collection("monitor_maintenance").update(record.id, data)
			} else {
				await pb.collection("monitor_maintenance").create(data)
			}
			onClose()
		} catch (err) {
			toast({ variant: "destructive", title: t`Failed to save maintenance`, description: getErrorMessage(err) })
		} finally {
			setSaving(false)
		}
	}

	return (
		<DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
			<DialogHeader>
				<DialogTitle>{record ? <Trans>Edit maintenance</Trans> : <Trans>Add maintenance</Trans>}</DialogTitle>
				<DialogDescription>
					<Trans>Selected monitors are shown as under maintenance during this window.</Trans>
				</DialogDescription>
			</DialogHeader>
			<form onSubmit={handleSubmit} className="grid gap-4 min-w-0">
				<div className="grid gap-2">
					<Label htmlFor="maint-title">
						<Trans>Title</Trans>
					</Label>
					<Input id="maint-title" value={title} onChange={(e) => setTitle(e.target.value)} required maxLength={200} />
				</div>
				<div className="grid gap-2">
					<Label htmlFor="maint-description">
						<Trans>Description</Trans>
					</Label>
					<Textarea
						id="maint-description"
						value={description}
						onChange={(e) => setDescription(e.target.value)}
						rows={2}
						maxLength={2000}
					/>
				</div>
				<div className="grid gap-2">
					<Label htmlFor="maint-type">
						<Trans>Type</Trans>
					</Label>
					<Select value={windowType} onValueChange={(value: MaintenanceRecord["type"]) => setWindowType(value)}>
						<SelectTrigger id="maint-type">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="one-time">
								<Trans>One-time</Trans>
							</SelectItem>
							<SelectItem value="daily">
								<Trans>Daily</Trans>
							</SelectItem>
						</SelectContent>
					</Select>
				</div>
				{windowType === "one-time" ? (
					<div className="grid gap-4 sm:grid-cols-2">
						<div className="grid gap-2">
							<Label htmlFor="maint-start">
								<Trans>Start Time</Trans>
							</Label>
							<Input
								id="maint-start"
								type="datetime-local"
								value={startDateTime}
								onChange={(e) => setStartDateTime(e.target.value)}
								required
								className="tabular-nums tracking-tighter"
							/>
						</div>
						<div className="grid gap-2">
							<Label htmlFor="maint-end">
								<Trans>End Time</Trans>
							</Label>
							<Input
								id="maint-end"
								type="datetime-local"
								value={endDateTime}
								onChange={(e) => setEndDateTime(e.target.value)}
								min={startDateTime}
								required
								className="tabular-nums tracking-tighter"
							/>
						</div>
					</div>
				) : (
					<div className="grid gap-4 grid-cols-2">
						<div className="grid gap-2">
							<Label htmlFor="maint-start-time">
								<Trans>Start Time</Trans>
							</Label>
							<Input
								id="maint-start-time"
								type="time"
								value={startTime}
								onChange={(e) => setStartTime(e.target.value)}
								required
								className="tabular-nums tracking-tighter"
							/>
						</div>
						<div className="grid gap-2">
							<Label htmlFor="maint-end-time">
								<Trans>End Time</Trans>
							</Label>
							<Input
								id="maint-end-time"
								type="time"
								value={endTime}
								onChange={(e) => setEndTime(e.target.value)}
								required
								className="tabular-nums tracking-tighter"
							/>
						</div>
						<p className="col-span-2 -mt-2 text-xs text-muted-foreground">
							<Trans>Repeats every day. Windows may span midnight.</Trans>
						</p>
					</div>
				)}
				<div className="grid gap-2 min-w-0">
					<Label>
						<Trans>Monitors</Trans>
					</Label>
					<MonitorChecklist monitors={monitors} selected={selected} onChange={setSelected} />
				</div>
				<div className="flex items-center justify-between gap-4">
					<Label htmlFor="maint-show" className="grid gap-1 font-normal">
						<span className="font-medium">
							<Trans>Show on status pages</Trans>
						</span>
						<span className="text-xs text-muted-foreground">
							<Trans>Status pages that include these monitors show this notice.</Trans>
						</span>
					</Label>
					<Switch id="maint-show" checked={showOnStatusPages} onCheckedChange={setShowOnStatusPages} />
				</div>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						<Trans>Cancel</Trans>
					</Button>
					<Button type="submit" disabled={saving}>
						{record ? <Trans>Update</Trans> : <Trans>Create</Trans>}
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}

/** Searchable checkbox list of monitors. */
function MonitorChecklist({
	monitors,
	selected,
	onChange,
}: {
	monitors: NetworkMonitorRecord[]
	selected: string[]
	onChange: (ids: string[]) => void
}) {
	const [search, setSearch] = useState("")
	const selectedSet = new Set(selected)
	const query = search.trim().toLocaleLowerCase()
	const filtered = monitors
		.filter((m) => !query || `${m.name} ${m.target}`.toLocaleLowerCase().includes(query))
		.sort((a, b) => getMonitorName(a).localeCompare(getMonitorName(b)))
	const missing = selected.filter((id) => !monitors.some((m) => m.id === id)).length

	const toggle = (id: string, checked: boolean) => {
		onChange(checked ? [...selected, id] : selected.filter((s) => s !== id))
	}
	const setFiltered = (checked: boolean) => {
		if (!checked && !query) {
			onChange([])
			return
		}
		const ids = new Set(filtered.map((m) => m.id))
		const rest = selected.filter((id) => !ids.has(id))
		onChange(checked ? [...rest, ...ids] : rest)
	}

	return (
		<div className="rounded-md border min-w-0">
			<div className="flex items-center gap-2 border-b px-3">
				<SearchIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
				<Input
					value={search}
					onChange={(e) => setSearch(e.target.value)}
					placeholder={t`Search monitors`}
					aria-label={t`Search monitors`}
					className="h-9 min-w-0 rounded-none border-0 bg-transparent px-0 shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
					onKeyDown={(e) => e.key === "Enter" && e.preventDefault()}
				/>
			</div>
			<div className="flex items-center justify-between gap-2 px-3 py-1.5 text-xs text-muted-foreground border-b">
				<div className="flex gap-3">
					<button type="button" className="hover:text-foreground" onClick={() => setFiltered(true)}>
						{query ? <Trans>Select matches</Trans> : <Trans>Select all</Trans>}
					</button>
					<button type="button" className="hover:text-foreground" onClick={() => setFiltered(false)}>
						{query ? <Trans>Clear matches</Trans> : <Trans>Clear all</Trans>}
					</button>
				</div>
				<span className="tabular-nums">{t`${selected.length} selected`}</span>
			</div>
			<div className="max-h-52 overflow-y-auto py-1">
				{filtered.length === 0 && (
					<p className="px-3 py-3 text-sm text-muted-foreground">
						<Trans>No monitors found.</Trans>
					</p>
				)}
				{filtered.map((monitor) => {
					const id = `maint-monitor-${monitor.id}`
					const target = getMonitorTarget(monitor)
					return (
						<label
							key={monitor.id}
							htmlFor={id}
							className="flex items-center gap-2.5 px-3 py-1.5 cursor-pointer hover:bg-accent/50"
						>
							<Checkbox
								id={id}
								checked={selectedSet.has(monitor.id)}
								onCheckedChange={(checked) => toggle(monitor.id, checked === true)}
							/>
							<span className="min-w-0 truncate text-sm">{getMonitorName(monitor)}</span>
							{monitor.name && target && (
								<span className="min-w-0 truncate text-xs text-muted-foreground">{target}</span>
							)}
						</label>
					)
				})}
				{missing > 0 && (
					<p className="px-3 py-1.5 text-xs text-muted-foreground">
						<Plural
							value={missing}
							one="# selected monitor is unavailable"
							other="# selected monitors are unavailable"
						/>
					</p>
				)}
			</div>
		</div>
	)
}
