import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { formatRelativeTime, getMonitorStatus, monitorStatusBadgeColors } from "@/lib/network-monitor-utils"
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

type BadgeMonitor = Pick<NetworkMonitorRecord, "status" | "enabled" | "statusChanged" | "lastError" | "lastStatusCode">

/** Status badge of a monitor with a tooltip showing the last error and when the status changed. */
export function MonitorStatusBadge({
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
	if (!tooltip || (!showError && !monitor.statusChanged)) {
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
				{monitor.statusChanged && (
					<p className={cn("text-muted-foreground text-xs", showError && "mt-1")}>
						<Trans>
							Since {formatRelativeTime(monitor.statusChanged)} ({formatShortDate(monitor.statusChanged)})
						</Trans>
					</p>
				)}
			</TooltipContent>
		</Tooltip>
	)
}
