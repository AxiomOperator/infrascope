import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { UnreachableBadge } from "@/components/unreachable-badge"
import { isUnreachable } from "@/lib/monitor-dependencies"
import { getLocationName, getLocationStatuses, type MonitorLocationState } from "@/lib/monitor-locations"
import {
	formatRelativeTime,
	getMonitorStatus,
	monitorStatusBadgeColors,
	monitorStatusBgColors,
} from "@/lib/network-monitor-utils"
import { $allSystemsById } from "@/lib/stores"
import { cn, formatShortDate } from "@/lib/utils"
import type { MonitorStatus, NetworkMonitorRecord } from "@/types"

export function monitorStatusLabel(status: MonitorStatus | "") {
	switch (status) {
		case "up":
			return t`Up`
		case "down":
			return t`Down`
		case "pending":
			return t`Pending`
		case "maintenance":
			return t`Maintenance`
		case "paused":
			return t`Paused`
		default:
			return t`Unknown`
	}
}

type BadgeMonitor = Pick<
	NetworkMonitorRecord,
	"status" | "enabled" | "statusChanged" | "lastError" | "lastStatusCode"
> &
	Partial<Pick<NetworkMonitorRecord, "system" | "locations" | "locationStatus" | "suppressedBy">>

/** Status of each location of a multi-location monitor, for tooltips. */
export function LocationStatusList({ states, className }: { states: MonitorLocationState[]; className?: string }) {
	const systems = useStore($allSystemsById)
	const hubName = t`Hub`
	return (
		<ul className={cn("grid gap-0.5", className)}>
			{states.map((state) => (
				<li key={state.location} className="flex items-center gap-1.5 min-w-0">
					<span className={cn("size-2 shrink-0 rounded-full", monitorStatusBgColors[state.status])} />
					<span className="truncate">{getLocationName(state.location, systems, hubName)}</span>
					<span className="ms-auto ps-2 text-muted-foreground shrink-0">{monitorStatusLabel(state.status)}</span>
				</li>
			))}
		</ul>
	)
}

/**
 * Status badge of a monitor with a tooltip showing the last error, the status of each location
 * of multi-location monitors and when the status changed. Monitors behind a down parent also
 * get an "Unreachable" badge.
 */
export function MonitorStatusBadge(props: { monitor: BadgeMonitor; className?: string; tooltip?: boolean }) {
	if (!isUnreachable(props.monitor)) {
		return <StatusBadge {...props} />
	}
	return (
		<span className="inline-flex flex-wrap items-center gap-1.5">
			<StatusBadge {...props} />
			<UnreachableBadge record={props.monitor} />
		</span>
	)
}

function StatusBadge({
	monitor,
	className,
	tooltip = true,
}: {
	monitor: BadgeMonitor
	className?: string
	tooltip?: boolean
}) {
	const status = getMonitorStatus(monitor)
	const badge = (
		<Badge className={cn("pointer-events-auto", monitorStatusBadgeColors[status], className)}>
			{monitorStatusLabel(status)}
		</Badge>
	)
	const showError = (status === "down" || status === "pending") && !!monitor.lastError
	const locationStates = monitor.system !== undefined ? getLocationStatuses({ ...monitor, system: monitor.system }) : []
	const showLocations = locationStates.length > 1
	if (!tooltip || (!showError && !monitor.statusChanged && !showLocations)) {
		return badge
	}
	return (
		<Tooltip>
			<TooltipTrigger asChild>{badge}</TooltipTrigger>
			<TooltipContent className="max-w-80 text-start">
				{showError && (
					<p className="break-words">
						{monitor.lastStatusCode ? <span className="tabular-nums">{monitor.lastStatusCode} · </span> : null}
						{monitor.lastError}
					</p>
				)}
				{showLocations && <LocationStatusList states={locationStates} className={cn(showError && "mt-1.5")} />}
				{monitor.statusChanged && (
					<p className={cn("text-muted-foreground text-xs", (showError || showLocations) && "mt-1")}>
						<Trans>
							Since {formatRelativeTime(monitor.statusChanged)} ({formatShortDate(monitor.statusChanged)})
						</Trans>
					</p>
				)}
			</TooltipContent>
		</Tooltip>
	)
}
