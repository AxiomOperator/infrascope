import { t } from "@lingui/core/macro"
import { Plural, Trans } from "@lingui/react/macro"
import type { ColumnDef } from "@tanstack/react-table"
import { MessageSquareTextIcon } from "lucide-react"
import { useState } from "react"
import { AckBadge, AlertAckPanel } from "@/components/alert-ack"
import { SeverityBadge } from "@/components/notification-channels"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { getAlertInfo } from "@/lib/alerts"
import { effectiveSeverity } from "@/lib/notification-channels"
import { cn, formatDuration, formatShortDate, toFixedFloat } from "@/lib/utils"
import type { AlertsHistoryRecord } from "@/types"

/** Name of the monitor of a monitor alert, if any. */
export function getAlertMonitorName(record: AlertsHistoryRecord): string {
	const monitor = record.expand?.monitor as { name?: string; target?: string } | undefined
	return record.monitor_name || monitor?.name || monitor?.target || ""
}

/** Display name of an alert history record, e.g. "Monitor down: API". */
export function getAlertHistoryName(record: AlertsHistoryRecord): string {
	const label = getAlertInfo(record.name)?.name().replace("cpu", "CPU") || record.name
	const monitorName = getAlertMonitorName(record)
	return monitorName ? `${label}: ${monitorName}` : label
}

/** Detail dialog of a history row with its acknowledgement and notes. */
function AlertHistoryDetails({ record }: { record: AlertsHistoryRecord }) {
	const [open, setOpen] = useState(false)
	const system = record.expand?.system?.name
	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<Button variant="ghost" size="icon" className="size-8" onClick={() => setOpen(true)} aria-label={t`Details`}>
				<MessageSquareTextIcon className="size-4" />
			</Button>
			{open && (
				<DialogContent className="max-h-[90vh] overflow-auto">
					<DialogHeader>
						<DialogTitle>{getAlertHistoryName(record)}</DialogTitle>
						<DialogDescription>
							{system ? `${system} · ` : ""}
							{formatShortDate(record.created)}
							{record.resolved ? ` – ${formatShortDate(record.resolved)}` : ""}
						</DialogDescription>
					</DialogHeader>
					<AlertAckPanel record={record} />
				</DialogContent>
			)}
		</Dialog>
	)
}

export const alertsHistoryColumns: ColumnDef<AlertsHistoryRecord>[] = [
	{
		accessorKey: "system",
		enableSorting: true,
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans>System</Trans>
			</Button>
		),
		cell: ({ row }) => {
			const name = row.original.expand?.system?.name || row.original.system
			if (!name) {
				return <div className="ps-2 text-muted-foreground">—</div>
			}
			return <div className="ps-2 max-w-60 truncate">{name}</div>
		},
		filterFn: (row, _, filterValue) => {
			const display = row.original.expand?.system?.name || row.original.system || ""
			return display.toLowerCase().includes(filterValue.toLowerCase())
		},
	},
	{
		// accessorKey: "name",
		id: "name",
		accessorFn: getAlertHistoryName,
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans>Name</Trans>
			</Button>
		),
		cell: ({ getValue, row }) => {
			const name = getValue() as string
			const info = getAlertInfo(row.original.name)
			const Icon = info?.icon

			return (
				<span className="flex items-center gap-2 ps-1 min-w-40">
					{Icon && <Icon className="size-3.5" />}
					{name}
					<SeverityBadge severity={effectiveSeverity(row.original)} />
				</span>
			)
		},
	},
	{
		accessorKey: "value",
		enableSorting: false,
		header: () => (
			<Button variant="ghost">
				<Trans>Value</Trans>
			</Button>
		),
		cell({ row, getValue }) {
			const name = row.original.name
			const info = getAlertInfo(name)
			if (name === "MonitorCert") {
				const days = Math.round(getValue() as number)
				return (
					<span className="tabular-nums ps-2.5">
						<Plural value={days} one="# day" other="# days" />
					</span>
				)
			}
			if (info?.triggeredDesc) {
				return <span className="ps-2">{info.triggeredDesc()}</span>
			}
			if (name === "Status" || name === "MonitorDown") {
				return <span className="ps-2">{t`Down`}</span>
			}
			const value = getValue() as number
			const unit = info?.unit
			return (
				<span className="tabular-nums ps-2.5">
					{toFixedFloat(value, value < 10 ? 2 : 1)}
					{unit}
				</span>
			)
		},
	},
	{
		accessorKey: "state",
		enableSorting: true,
		sortingFn: (rowA, rowB) => (rowA.original.resolved ? 1 : 0) - (rowB.original.resolved ? 1 : 0),
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans comment="Context: alert state (active or resolved)">State</Trans>
			</Button>
		),
		cell: ({ row }) => {
			const resolved = row.original.resolved
			return (
				<Badge
					className={cn(
						"capitalize pointer-events-none",
						resolved
							? "bg-green-100 text-green-800 border-green-200 dark:opacity-80"
							: "bg-yellow-100 text-yellow-800 border-yellow-200"
					)}
				>
					{/* {resolved ? <CircleCheckIcon className="size-3 me-0.5" /> : <CircleAlertIcon className="size-3 me-0.5" />} */}
					{resolved ? <Trans>Resolved</Trans> : <Trans>Active</Trans>}
				</Badge>
			)
		},
	},
	{
		accessorKey: "created",
		accessorFn: (record) => formatShortDate(record.created),
		enableSorting: true,
		invertSorting: true,
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans comment="Context: date created">Created</Trans>
			</Button>
		),
		cell: ({ getValue, row }) => (
			<span className="ps-1 tabular-nums tracking-tight" title={`${row.original.created} UTC`}>
				{getValue() as string}
			</span>
		),
	},
	{
		accessorKey: "resolved",
		enableSorting: true,
		invertSorting: true,
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans>Resolved</Trans>
			</Button>
		),
		cell: ({ row, getValue }) => {
			const resolved = getValue() as string | null
			if (!resolved) {
				return null
			}
			return (
				<span className="ps-1 tabular-nums tracking-tight" title={`${row.original.resolved} UTC`}>
					{formatShortDate(resolved)}
				</span>
			)
		},
	},
	{
		accessorKey: "duration",
		invertSorting: true,
		enableSorting: true,
		sortingFn: (rowA, rowB) => {
			const aCreated = new Date(rowA.original.created)
			const bCreated = new Date(rowB.original.created)
			const aResolved = rowA.original.resolved ? new Date(rowA.original.resolved) : null
			const bResolved = rowB.original.resolved ? new Date(rowB.original.resolved) : null
			const aDuration = aResolved ? aResolved.getTime() - aCreated.getTime() : null
			const bDuration = bResolved ? bResolved.getTime() - bCreated.getTime() : null
			if (!aDuration && bDuration) return -1
			if (aDuration && !bDuration) return 1
			return (aDuration || 0) - (bDuration || 0)
		},
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans>Duration</Trans>
			</Button>
		),
		cell: ({ row }) => {
			const duration = formatDuration(row.original.created, row.original.resolved)
			if (!duration) {
				return null
			}
			return <span className="ps-2">{duration}</span>
		},
	},
	{
		id: "acknowledged",
		accessorFn: (record) => record.acknowledgedAt ?? "",
		enableSorting: true,
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				<Trans>Acknowledged</Trans>
			</Button>
		),
		cell: ({ row }) => (
			<span className="flex items-center gap-1 ps-1">
				<AckBadge record={row.original} />
				<AlertHistoryDetails record={row.original} />
			</span>
		),
	},
]
