import type { CellContext, Column, ColumnDef } from "@tanstack/react-table"
import { Button } from "@/components/ui/button"
import { getMonitorProtocolLabel } from "@/lib/monitor-protocols"
import { cn, copyToClipboard, decimalString, formatMicroseconds, hourWithSeconds } from "@/lib/utils"
import {
	TimerIcon,
	WifiOffIcon,
	Trash2Icon,
	ArrowLeftRightIcon,
	MoreHorizontalIcon,
	ServerIcon,
	ClockIcon,
	RefreshCwIcon,
	PenBoxIcon,
	PauseCircleIcon,
	PlayCircleIcon,
	CopyIcon,
	CopyPlusIcon,
	ShieldCheckIcon,
	ActivityIcon,
	CircleDotIcon,
	PercentIcon,
	TagIcon,
} from "lucide-react"
import { t } from "@lingui/core/macro"
import type { NetworkMonitorRecord, SystemRecord } from "@/types"
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Plural, Trans } from "@lingui/react/macro"
import { $allSystemsById } from "@/lib/stores"
import type { ReadableAtom } from "nanostores"
import { useStore } from "@nanostores/react"
import { SystemStatus } from "@/lib/enums"
import { Checkbox } from "@/components/ui/checkbox"
import { useMemo } from "react"
import { formatBulkMonitorLine } from "@/components/network-monitors-table/monitor-form-utils"
import { Badge } from "../ui/badge"
import {
	formatUptime,
	getCertDaysLeft,
	getCertExpiryLevel,
	getMonitorName,
	getMonitorRecent,
	getMonitorStatus,
	getMonitorTarget,
	getMonitorUptime,
	monitorStatusBgColors,
} from "@/lib/network-monitor-utils"
import { pb } from "@/lib/api"
import { getLocationName, getMonitorLocations, isMultiLocation } from "@/lib/monitor-locations"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { MonitorStatusBadge } from "./monitor-status-badge"
import { MonitorStatusBar } from "./status-bar"

const certExpiryDotColors = { ok: "bg-green-500", warning: "bg-yellow-500", critical: "bg-red-500" }

declare module "@tanstack/react-table" {
	interface ColumnMeta<TData, TValue> {
		label?: string
	}
}

const protocolColors: Record<string, string> = {
	icmp: "bg-blue-500/15! text-blue-600 dark:text-blue-400",
	tcp: "bg-purple-500/15! text-purple-600 dark:text-purple-400",
	http: "bg-green-500/15! text-green-700 dark:text-green-400",
	dns: "bg-amber-500/15! text-amber-600 dark:text-amber-400",
	push: "bg-cyan-500/15! text-cyan-700 dark:text-cyan-400",
	ssh: "bg-slate-500/15! text-slate-700 dark:text-slate-300",
	grpc: "bg-emerald-500/15! text-emerald-700 dark:text-emerald-400",
	postgres: "bg-sky-500/15! text-sky-700 dark:text-sky-400",
	mysql: "bg-orange-500/15! text-orange-700 dark:text-orange-400",
	redis: "bg-red-500/15! text-red-600 dark:text-red-400",
	smtp: "bg-indigo-500/15! text-indigo-600 dark:text-indigo-400",
	imap: "bg-violet-500/15! text-violet-600 dark:text-violet-400",
	minecraft: "bg-lime-500/15! text-lime-700 dark:text-lime-400",
	a2s: "bg-yellow-500/15! text-yellow-700 dark:text-yellow-400",
	docker: "bg-blue-500/15! text-blue-700 dark:text-blue-300",
}

const SYSTEM_STATUS_COLORS = {
	[SystemStatus.Up]: "bg-green-500",
	[SystemStatus.Down]: "bg-red-500",
	[SystemStatus.Paused]: "bg-primary/40",
	[SystemStatus.Pending]: "bg-yellow-500",
} as const

/**
 * A monitor is considered muted if it's disabled or if its only agent system is not up.
 * Hub monitors don't depend on a system, and multi-location monitors leave out unreachable agents.
 */
const isMuted = (record: NetworkMonitorRecord, systemRecord: SystemRecord | undefined) =>
	!record.enabled || (!!record.system && !isMultiLocation(record) && systemRecord?.status !== SystemStatus.Up)

const statusOrder: Record<string, number> = { down: 0, pending: 1, unknown: 2, maintenance: 3, up: 4, paused: 5 }

export function getMonitorColumns(
	longestTarget = "",
	$longestSystemName: ReadableAtom<string>,
	{
		onEdit,
		onDelete,
		onSetEnabled,
	}: {
		onEdit?: (monitor: NetworkMonitorRecord) => void
		onDelete?: (monitors: NetworkMonitorRecord[]) => void | Promise<void>
		onSetEnabled?: (monitors: NetworkMonitorRecord[], enabled: boolean) => void | Promise<void>
	} = {}
): ColumnDef<NetworkMonitorRecord>[] {
	return [
		{
			id: "select",
			header: ({ table }) => (
				<Checkbox
					className="ms-2"
					checked={table.getIsAllRowsSelected() || (table.getIsSomeRowsSelected() && "indeterminate")}
					onClick={(event) => event.stopPropagation()}
					onCheckedChange={(value) => table.toggleAllRowsSelected(!!value)}
					aria-label={t`Select all`}
				/>
			),
			cell: ({ row }) => (
				<Checkbox
					checked={row.getIsSelected()}
					onClick={(event) => event.stopPropagation()}
					onCheckedChange={(value) => row.toggleSelected(!!value)}
					aria-label={t`Select row`}
				/>
			),
			enableSorting: false,
			enableHiding: false,
			size: 44,
		},
		{
			id: "system",
			meta: { label: t`System` },
			accessorFn: (record) => record.system,
			sortingFn: (a, b) => {
				const allSystems = $allSystemsById.get()
				// hub monitors sort before agent monitors
				const systemNameA = a.original.system ? (allSystems[a.original.system]?.name ?? "") : ""
				const systemNameB = b.original.system ? (allSystems[b.original.system]?.name ?? "") : ""
				const primary = systemNameA.localeCompare(systemNameB)
				if (primary !== 0) {
					return primary
				}
				return getMonitorName(a.original).localeCompare(getMonitorName(b.original))
			},
			header: ({ column }) => <HeaderButton column={column} name={t`System`} Icon={ServerIcon} />,
			cell: ({ getValue, row }) => {
				const systemId = getValue() as string
				const allSystems = useStore($allSystemsById)
				const system = allSystems[systemId] as SystemRecord | undefined
				const longestSystemName = useStore($longestSystemName)
				const name = systemId ? system?.name : t`Hub`
				const status = system?.status as SystemStatus // undefined val is fine but makes lsp mad
				const locations = getMonitorLocations(row.original)
				const hubName = t`Hub`
				const locationKey = locations.join(",")

				return useMemo(
					() =>
						locations.length > 1 ? (
							<LocationChips locations={locations} systems={allSystems} hubName={hubName} />
						) : (
							<div className="ms-1.5 max-w-44 flex gap-2 items-center tabular-nums">
								<span
									className={cn(
										"shrink-0 size-2 rounded-full",
										systemId ? SYSTEM_STATUS_COLORS[status] : "bg-primary/40"
									)}
								/>
								<div className="relative w-fit min-w-0 max-w-full">
									<span className="invisible block whitespace-nowrap" aria-hidden="true">
										{longestSystemName}
									</span>
									<span className="absolute inset-0 truncate">{name}</span>
								</div>
							</div>
						),
					[status, name, longestSystemName, systemId, locationKey, allSystems, hubName]
				)
			},
		},
		{
			// id kept as "target" so saved sort and visibility settings still apply
			id: "target",
			meta: { label: t`Name` },
			sortingFn: (a, b) => getMonitorName(a.original).localeCompare(getMonitorName(b.original)),
			accessorFn: (record) => getMonitorName(record),
			header: ({ column }) => <HeaderButton column={column} name={t`Name`} Icon={TagIcon} />,
			cell: ({ row, getValue }) => {
				const monitor = row.original
				const { status: systemStatus } = useStore($allSystemsById)[monitor.system] || {}

				let color = monitorStatusBgColors[getMonitorStatus(monitor)]
				// agent monitors can't report while their system is unreachable
				if (monitor.enabled && monitor.system && !isMultiLocation(monitor) && systemStatus !== SystemStatus.Up) {
					color = systemStatus === SystemStatus.Paused ? monitorStatusBgColors.paused : "bg-yellow-500"
				}
				const target = monitor.name ? getMonitorTarget(monitor) : ""
				return (
					<div className="ms-1.5 max-w-64 flex gap-2 items-center tabular-nums">
						<span className={cn("shrink-0 size-2 rounded-full", color)} />
						<div className="relative w-fit min-w-0 max-w-full">
							<span className="invisible block overflow-hidden whitespace-nowrap" aria-hidden="true">
								{longestTarget}
							</span>
							<span className="absolute inset-0 truncate">{getValue() as string}</span>
							{target && <span className="block truncate text-xs text-muted-foreground leading-tight">{target}</span>}
						</div>
					</div>
				)
			},
		},
		{
			id: "status",
			meta: { label: t`Status` },
			accessorFn: (record) => statusOrder[getMonitorStatus(record)] ?? 2,
			header: ({ column }) => <HeaderButton column={column} name={t`Status`} Icon={CircleDotIcon} />,
			cell: ({ row }) => (
				<div className="ms-1.5">
					<MonitorStatusBadge monitor={row.original} />
				</div>
			),
		},
		{
			id: "recent",
			meta: { label: t`Recent checks` },
			enableSorting: false,
			header: ({ column }) => <HeaderButton column={column} name={t`Recent checks`} Icon={ActivityIcon} />,
			cell: ({ row }) => <MonitorStatusBar recent={getMonitorRecent(row.original)} className="ms-1.5 w-44" />,
		},
		{
			id: "uptime1d",
			meta: { label: t`Uptime 24h` },
			accessorFn: (record) => getMonitorUptime(record).d1 ?? undefined,
			header: ({ column }) => <HeaderButton column={column} name={t`Uptime 24h`} Icon={PercentIcon} />,
			cell: uptimeCell,
		},
		{
			id: "uptime30d",
			meta: { label: t`Uptime 30d` },
			accessorFn: (record) => getMonitorUptime(record).d30 ?? undefined,
			header: ({ column }) => <HeaderButton column={column} name={t`Uptime 30d`} Icon={PercentIcon} />,
			cell: uptimeCell,
		},
		{
			id: "protocol",
			meta: { label: t`Protocol` },
			accessorFn: (record) => record.protocol,
			header: ({ column }) => <HeaderButton column={column} name={t`Protocol`} Icon={ArrowLeftRightIcon} />,
			cell: ({ getValue }) => {
				const protocol = getValue() as string
				return <Badge className={protocolColors[protocol]}>{getMonitorProtocolLabel(protocol)}</Badge>
			},
		},
		{
			id: "interval",
			meta: { label: t`Interval` },
			accessorFn: (record) => record.interval,
			invertSorting: true,
			header: ({ column }) => <HeaderButton column={column} name={t`Interval`} Icon={RefreshCwIcon} />,
			cell: ({ getValue }) => <span className="ms-1.5 tabular-nums">{getValue() as number}s</span>,
		},
		{
			id: "res",
			meta: { label: t`Response` },
			accessorFn: (record) => record.res,
			invertSorting: true,
			header: ({ column }) => <HeaderButton column={column} name={t`Response`} Icon={TimerIcon} />,
			cell: responseTimeCell,
		},
		{
			id: "res1h",
			meta: { label: t`Avg 1h` },
			accessorFn: (record) => record.resAvg1h,
			invertSorting: true,
			header: ({ column }) => <HeaderButton column={column} name={t`Avg 1h`} Icon={TimerIcon} />,
			cell: responseTimeCell,
		},
		{
			id: "max1h",
			meta: { label: t`Max 1h` },
			accessorFn: (record) => record.resMax1h,
			invertSorting: true,
			header: ({ column }) => <HeaderButton column={column} name={t`Max 1h`} Icon={TimerIcon} />,
			cell: responseTimeCell,
		},
		{
			id: "min1h",
			meta: { label: t`Min 1h` },
			accessorFn: (record) => record.resMin1h,
			invertSorting: true,
			header: ({ column }) => <HeaderButton column={column} name={t`Min 1h`} Icon={TimerIcon} />,
			cell: responseTimeCell,
		},
		{
			id: "loss",
			meta: { label: t`Loss 1h` },
			accessorFn: (record) => record.loss1h,
			invertSorting: true,
			header: ({ column }) => <HeaderButton column={column} name={t`Loss 1h`} Icon={WifiOffIcon} />,
			cell: ({ row }) => {
				const { loss1h, res, system } = row.original
				const systemRecord = useStore($allSystemsById)[system]

				if (loss1h === undefined || (!res && !loss1h)) {
					return <span className="ms-1.5 text-muted-foreground">-</span>
				}

				const muted = isMuted(row.original, systemRecord)
				let color = "bg-green-500"
				if (muted) {
					color = "bg-muted-foreground/50"
				} else if (loss1h) {
					color = loss1h > 20 ? "bg-red-500" : "bg-yellow-500"
				}
				return (
					<span className="ms-1.5 tabular-nums flex gap-2 items-center">
						<span className={cn("shrink-0 size-2 rounded-full", color)} />
						{loss1h === 100 ? loss1h : decimalString(loss1h, loss1h >= 10 ? 1 : 2)}%
					</span>
				)
			},
		},
		{
			id: "cert",
			meta: { label: t`Certificate` },
			accessorFn: (record) => record.certInfo?.expires,
			header: ({ column }) => <HeaderButton column={column} name={t`Certificate`} Icon={ShieldCheckIcon} />,
			cell: ({ row }) => {
				const { certInfo, system } = row.original
				const systemRecord = useStore($allSystemsById)[system]

				if (!certInfo?.expires) {
					return <span className="ms-1.5 text-muted-foreground">-</span>
				}

				const daysLeft = getCertDaysLeft(certInfo)
				const color = isMuted(row.original, systemRecord)
					? "bg-muted-foreground/50"
					: certExpiryDotColors[getCertExpiryLevel(daysLeft)]
				return (
					<span className="ms-1.5 tabular-nums flex gap-2 items-center">
						<span className={cn("shrink-0 size-2 rounded-full", color)} />
						{daysLeft < 0 ? <Trans>Expired</Trans> : <Plural value={daysLeft} one="# day" other="# days" />}
					</span>
				)
			},
		},
		{
			id: "updated",
			meta: { label: t`Updated` },
			invertSorting: true,
			accessorFn: (record) => record.updated,
			header: ({ column }) => <HeaderButton column={column} name={t`Updated`} Icon={ClockIcon} />,
			cell: ({ getValue }) => {
				const timestamp = getValue() as number
				if (!timestamp) {
					return <span className="ms-1.5 text-muted-foreground">-</span>
				}
				return <span className="ms-1.5 tabular-nums">{hourWithSeconds(timestamp)}</span>
			},
		},
		{
			id: "actions",
			enableSorting: false,
			enableHiding: false,
			header: () => null,
			size: 40,
			cell: ({ row, table }) => {
				const selectedRows = table.getSelectedRowModel().rows
				const actionRows =
					row.getIsSelected() && selectedRows.length > 1
						? selectedRows.map((selectedRow) => selectedRow.original)
						: [row.original]
				const isBulkAction = actionRows.length > 1
				const shouldPause = actionRows.some((monitor) => monitor.enabled)
				// push monitors have no target and can't be expressed as bulk lines
				const bulkCopyContent = actionRows
					.filter((monitor) => monitor.protocol !== "push")
					.map((monitor) => formatBulkMonitorLine(monitor))
					.join("\n")
				const allSystems = useStore($allSystemsById)
				const otherSystems = useMemo(
					() =>
						Object.values(allSystems).filter(
							(s) => !isBulkAction && row.original.protocol !== "push" && s.id !== row.original.system
						),
					[allSystems, isBulkAction]
				)
				return (
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button variant="ghost" size="icon" className="size-10">
								<span className="sr-only">
									<Trans>Open menu</Trans>
								</span>
								<MoreHorizontalIcon className="w-5" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" onClick={(event) => event.stopPropagation()}>
							{!isBulkAction && (
								<DropdownMenuItem
									onClick={() => {
										onEdit?.(row.original)
									}}
								>
									<PenBoxIcon className="me-2.5 size-4" />
									<Trans>Edit</Trans>
								</DropdownMenuItem>
							)}
							<DropdownMenuItem
								onClick={() => {
									onSetEnabled?.(actionRows, !shouldPause)
								}}
							>
								{shouldPause ? (
									<>
										<PauseCircleIcon className="me-2.5 size-4" />
										<Trans>Pause</Trans>
									</>
								) : (
									<>
										<PlayCircleIcon className="me-2.5 size-4" />
										<Trans>Resume</Trans>
									</>
								)}
							</DropdownMenuItem>
							<DropdownMenuItem
								disabled={!bulkCopyContent}
								onClick={() => {
									copyToClipboard(bulkCopyContent)
								}}
							>
								<CopyIcon className="me-2.5 size-4" />
								<Trans>Bulk copy</Trans>
							</DropdownMenuItem>
							{!isBulkAction && otherSystems.length > 0 && (
								<DropdownMenuSub>
									<DropdownMenuSubTrigger>
										<CopyPlusIcon className="me-2.5 size-4" />
										<Trans>Copy to system</Trans>
									</DropdownMenuSubTrigger>
									<DropdownMenuSubContent className="max-h-[min(20rem,var(--radix-dropdown-menu-content-available-height))] overflow-y-auto">
										{otherSystems.map((sys) => (
											<DropdownMenuItem
												key={sys.id}
												onClick={() => {
													const {
														id: _id,
														system: _system,
														users: _users,
														pushToken: _pushToken,
														locations: _locations,
														locationSystems: _locationSystems,
														locationStatus: _locationStatus,
														quorum: _quorum,
														...rest
													} = row.original
													pb.collection("network_monitors")
														.create({ ...rest, system: sys.id, locations: [sys.id] })
														.catch(() => {})
												}}
											>
												{sys.name}
											</DropdownMenuItem>
										))}
									</DropdownMenuSubContent>
								</DropdownMenuSub>
							)}
							<DropdownMenuSeparator />
							<DropdownMenuItem
								onClick={() => {
									onDelete?.(actionRows)
								}}
							>
								<Trash2Icon className="me-2.5 size-4" />
								<Trans>Delete</Trans>
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
				)
			},
		},
	]
}

const responseTimeThresholds: Record<NetworkMonitorRecord["protocol"], { warning: number; critical: number }> = {
	http: { warning: 800_000, critical: 3_000_000 },
	push: { warning: 800_000, critical: 3_000_000 },
	tcp: { warning: 500_000, critical: 2_000_000 },
	icmp: { warning: 100_000, critical: 500_000 },
	dns: { warning: 150_000, critical: 800_000 },
	ssh: { warning: 500_000, critical: 2_000_000 },
	grpc: { warning: 800_000, critical: 3_000_000 },
	postgres: { warning: 800_000, critical: 3_000_000 },
	mysql: { warning: 500_000, critical: 2_000_000 },
	redis: { warning: 500_000, critical: 2_000_000 },
	smtp: { warning: 1_500_000, critical: 5_000_000 },
	imap: { warning: 1_500_000, critical: 5_000_000 },
	minecraft: { warning: 500_000, critical: 2_000_000 },
	a2s: { warning: 300_000, critical: 1_500_000 },
	docker: { warning: 500_000, critical: 2_000_000 },
}

function responseTimeCell(cell: CellContext<NetworkMonitorRecord, unknown>) {
	const monitor = cell.row.original
	const systemRecord = useStore($allSystemsById)[monitor.system]
	const responseTime = cell.getValue() as number | undefined

	if (!responseTime) {
		return <span className="ms-1.5 text-muted-foreground">-</span>
	}

	const muted = isMuted(monitor, systemRecord)
	let color = "bg-green-500"
	if (muted) {
		color = "bg-muted-foreground/50"
	} else if (responseTime > responseTimeThresholds[monitor.protocol].warning) {
		color = "bg-yellow-500"
	}
	if (!muted && responseTime > responseTimeThresholds[monitor.protocol].critical) {
		color = "bg-red-500"
	}
	return (
		<span className="ms-1.5 tabular-nums flex gap-2 items-center">
			<span className={cn("shrink-0 size-2 rounded-full", color)} />
			{formatMicroseconds(responseTime)}
		</span>
	)
}

function uptimeCell(cell: CellContext<NetworkMonitorRecord, unknown>) {
	const value = cell.getValue() as number | undefined
	if (value == null) {
		return <span className="ms-1.5 text-muted-foreground">—</span>
	}
	let color = "bg-green-500"
	if (!cell.row.original.enabled) {
		color = "bg-muted-foreground/50"
	} else if (value < 95) {
		color = "bg-red-500"
	} else if (value < 99.5) {
		color = "bg-yellow-500"
	}
	return (
		<span className="ms-1.5 tabular-nums flex gap-2 items-center">
			<span className={cn("shrink-0 size-2 rounded-full", color)} />
			{formatUptime(value)}
		</span>
	)
}

function HeaderButton({
	column,
	name,
	Icon,
}: {
	column: Column<NetworkMonitorRecord>
	name: string
	Icon: React.ElementType
}) {
	const isSorted = column.getIsSorted()
	return (
		<Button
			className={cn(
				"h-9 px-3 flex items-center gap-2 duration-50",
				isSorted && "bg-accent/70 light:bg-accent text-accent-foreground/90"
			)}
			variant="ghost"
			onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
		>
			{Icon && <Icon className="size-4" />}
			{name}
		</Button>
	)
}

/** Compact chips of a multi-location monitor's locations: the first ones and a count of the rest. */
function LocationChips({
	locations,
	systems,
	hubName,
}: {
	locations: string[]
	systems: Record<string, SystemRecord | undefined>
	hubName: string
}) {
	const shown = locations.slice(0, 2)
	const names = locations.map((location) => getLocationName(location, systems, hubName))
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<div className="ms-1.5 max-w-44 flex gap-1 items-center min-w-0">
					{shown.map((location, i) => (
						<Badge key={location} variant="outline" className="min-w-0 max-w-20 px-1.5 font-normal">
							<span className="truncate">{names[i]}</span>
						</Badge>
					))}
					{locations.length > shown.length && (
						<Badge variant="outline" className="shrink-0 px-1.5 font-normal tabular-nums">
							+{locations.length - shown.length}
						</Badge>
					)}
				</div>
			</TooltipTrigger>
			<TooltipContent className="max-w-80 text-start">
				<p className="font-medium">
					<Plural value={locations.length} one="# location" other="# locations" />
				</p>
				<p className="text-muted-foreground">{names.join(", ")}</p>
			</TooltipContent>
		</Tooltip>
	)
}
