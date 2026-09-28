import { t } from "@lingui/core/macro"
import { memo } from "react"
import { HoverBars } from "@/components/ui/hover-bars"
import { hourWithSeconds } from "@/lib/utils"
import { MONITOR_RECENT_SLOTS } from "@/lib/network-monitor-utils"
import type { MonitorRecentCheck } from "@/types"

const checkColors = ["bg-red-500", "bg-green-500", "bg-amber-500"]

function checkStateLabel(state: number) {
	if (state === 1) return t`Up`
	if (state === 2) return t`Pending`
	return t`Down`
}

function getCheckColor([, state]: MonitorRecentCheck) {
	return checkColors[state] ?? "bg-muted-foreground/40"
}

function CheckTooltip({ check: [time, state, responseMs] }: { check: MonitorRecentCheck }) {
	return (
		<div className="grid gap-0.5 tabular-nums">
			<span className="text-muted-foreground">{hourWithSeconds(time * 1000)}</span>
			<span className="flex items-center gap-1.5 font-medium">
				<span className={`size-2 rounded-full ${checkColors[state] ?? "bg-muted-foreground/40"}`} />
				{checkStateLabel(state)}
				{responseMs >= 0 && <span className="font-normal text-muted-foreground">· {responseMs} ms</span>}
			</span>
		</div>
	)
}

/** Recent checks of a monitor as colored bars, newest on the right. */
export const MonitorStatusBar = memo(function MonitorStatusBar({
	recent,
	slots = MONITOR_RECENT_SLOTS,
	className,
}: {
	recent: MonitorRecentCheck[]
	slots?: number
	className?: string
}) {
	return (
		<HoverBars
			items={recent}
			slots={slots}
			label={t`Recent checks`}
			className={className}
			getBarClassName={getCheckColor}
			renderTooltip={(check) => <CheckTooltip check={check} />}
		/>
	)
})
