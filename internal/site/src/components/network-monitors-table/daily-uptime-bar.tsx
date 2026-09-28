import { t } from "@lingui/core/macro"
import { Plural, Trans } from "@lingui/react/macro"
import { HoverBars } from "@/components/ui/hover-bars"
import { formatDurationMs, formatUptime } from "@/lib/network-monitor-utils"
import type { MonitorDayUptime } from "@/lib/use-monitor-events"

const dayFormatter = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric" })

function getDayColor({ uptime }: MonitorDayUptime) {
	if (uptime == null) return "bg-muted"
	if (uptime >= 99.9) return "bg-green-500"
	if (uptime >= 99) return "bg-lime-500"
	if (uptime >= 95) return "bg-amber-500"
	return "bg-red-500"
}

function DayTooltip({ day }: { day: MonitorDayUptime }) {
	return (
		<div className="grid gap-0.5 tabular-nums">
			<span className="text-muted-foreground">{dayFormatter.format(day.start)}</span>
			{day.uptime == null ? (
				<span>
					<Trans>No data</Trans>
				</span>
			) : (
				<>
					<span className="font-medium">
						<Trans>{formatUptime(day.uptime)} uptime</Trans>
					</span>
					{day.downMs > 0 && (
						<span>
							<Plural value={day.incidents} one="# incident" other="# incidents" /> ·{" "}
							<Trans>{formatDurationMs(day.downMs)} down</Trans>
						</span>
					)}
				</>
			)}
		</div>
	)
}

/** Daily uptime of the last days as colored bars, today on the right. */
export function DailyUptimeBar({ days, className }: { days: MonitorDayUptime[]; className?: string }) {
	return (
		<HoverBars
			items={days}
			label={t`Daily uptime`}
			className={className}
			barClassName="rounded-xs"
			getBarClassName={getDayColor}
			renderTooltip={(day) => <DayTooltip day={day} />}
		/>
	)
}
