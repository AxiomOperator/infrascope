import { getMonitorName, monitorGapRecord } from "@/lib/network-monitor-utils"
import LineChartDefault, { isolatedDot } from "@/components/charts/line-chart"
import type { DataPoint } from "@/components/charts/line-chart"
import { useYAxisWidth } from "@/components/charts/hooks"
import {
	ChartContainer,
	ChartLegend,
	ChartLegendContent,
	ChartTooltip,
	ChartTooltipContent,
} from "@/components/ui/chart"
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from "recharts"
import {
	chartMargin,
	cn,
	decimalString,
	formatMicroseconds,
	formatShortDate,
	hourWithSeconds,
	matchesFilterGroups,
	parseFilterGroups,
	toFixedFloat,
} from "@/lib/utils"
import { $monitorFilter } from "@/lib/stores"
import { useLingui } from "@lingui/react/macro"
import { ChartCard, FilterBar } from "../chart-card"
import type {
	ChartData,
	MonitorRecentCheck,
	MonitorStats,
	NetworkMonitorRecord,
	NetworkMonitorStatsRecord,
} from "@/types"
import { useMemo } from "react"
import { useStore } from "@nanostores/react"

type MonitorChartProps = {
	monitorStats: NetworkMonitorStatsRecord[]
	grid?: boolean
	monitors: NetworkMonitorRecord[]
	chartData: ChartData
	empty: boolean
	showFilter?: boolean
	/** Prepended to the chart title, e.g. a target/system name (rendered as "{titlePrefix} — Response"). */
	titlePrefix?: string
}

type MonitorChartBaseProps = MonitorChartProps & {
	metric: keyof MonitorStats
	title: string
	description: string
	tickFormatter: (value: number) => string
	contentFormatter: ({ value }: { value: number | string }) => string | number
	domain?: [number | "auto", number | "auto"]
	/** Overrides the per-monitor line colors (e.g. a fixed color for single-monitor charts). */
	color?: string
}

function MonitorChart({
	monitorStats,
	grid,
	monitors,
	chartData,
	empty,
	metric,
	title,
	description,
	tickFormatter,
	contentFormatter,
	domain,
	color,
	showFilter = monitors.length > 1,
}: MonitorChartBaseProps) {
	const storedFilter = useStore($monitorFilter)
	const filter = showFilter ? storedFilter : ""

	const { dataPoints, visibleKeys } = useMemo(() => {
		const sortedMonitors = [...monitors].sort((a, b) => b.resAvg1h - a.resAvg1h)
		const count = sortedMonitors.length
		const points: DataPoint<NetworkMonitorStatsRecord>[] = []
		const visibleIDs: string[] = []
		const filterGroups = parseFilterGroups(filter)
		const dot = chartData.chartTime === "1m"
		for (let i = 0; i < count; i++) {
			const p = sortedMonitors[i]
			const label = getMonitorName(p)
			const labelLower = label.toLowerCase()
			const filtered = filterGroups.length > 0 && !matchesFilterGroups(labelLower, filterGroups)
			if (filtered) {
				continue
			}
			visibleIDs.push(p.id)
			points.push({
				order: i,
				label,
				dataKey: (record: NetworkMonitorStatsRecord) => record.stats?.[p.id]?.[metric] ?? null,
				dot,
				color:
					color ?? (count <= 5 ? i + 1 : `hsl(${(i * 360) / count}, var(--chart-saturation), var(--chart-lightness))`),
			})
		}
		return { dataPoints: points, visibleKeys: visibleIDs }
	}, [monitors, filter, metric, chartData.chartTime, color])

	// Monitors with different intervals don't share timestamps, so multiple lines need connectNulls.
	// A single monitor's stats already contain empty records at real gaps, so the line breaks there.
	const multipleMonitors = visibleKeys.length > 1

	const filteredMonitorStats = useMemo(() => {
		if (!multipleMonitors) return monitorStats
		return monitorStats.filter((record) => visibleKeys.some((id) => record.stats?.[id] != null))
	}, [monitorStats, visibleKeys, multipleMonitors])

	const legend = dataPoints.length < 10 && showFilter

	return (
		<ChartCard
			legend={legend || !showFilter}
			cornerEl={showFilter ? <FilterBar store={$monitorFilter} /> : undefined}
			empty={empty}
			title={title}
			description={description}
			grid={grid}
		>
			<LineChartDefault
				truncate
				chartData={chartData}
				customData={filteredMonitorStats}
				dataPoints={dataPoints}
				domain={domain ?? ["auto", "auto"]}
				connectNulls={multipleMonitors}
				tickFormatter={tickFormatter}
				contentFormatter={contentFormatter}
				legend={legend}
				filter={filter}
			/>
		</ChartCard>
	)
}

interface AvgMinMaxResponseChartProps {
	monitorStats: NetworkMonitorStatsRecord[]
	monitor: NetworkMonitorRecord | null
	chartData: ChartData
	empty: boolean
}

export function AvgMinMaxResponseChart({ monitorStats, monitor, chartData, empty }: AvgMinMaxResponseChartProps) {
	const { t } = useLingui()

	const { chartTime } = chartData
	const hasLongInterval = (monitor?.interval ?? 61) > 60

	// only one monitor is relevant for this chart
	const dataPoints: DataPoint<NetworkMonitorStatsRecord>[] = useMemo(() => {
		const dataFn = (metric: keyof MonitorStats) => (record: NetworkMonitorStatsRecord) =>
			record.stats?.[monitor?.id ?? ""]?.[metric] ?? null
		const avgPoint = {
			label: "Avg",
			dot: isolatedDot,
			dataKey: dataFn("res_avg"),
			color: 1,
			order: 0,
		}
		if (chartTime === "1m" || (hasLongInterval && chartTime === "1h")) {
			// avg, min, max are all the same for 1m interval, so just show avg
			return [avgPoint]
		}
		return [
			{
				label: "Max",
				dot: isolatedDot,
				dataKey: dataFn("res_max"),
				color: 3,
				order: 0,
			},
			avgPoint,
			{
				label: "Min",
				dot: isolatedDot,
				dataKey: dataFn("res_min"),
				color: 2,
				order: 2,
			},
		]
	}, [chartTime, hasLongInterval, monitor?.id])

	// Replace records where every probe failed with gap markers, so the line breaks there without
	// leaving points that have no response time for the tooltip to show.
	const data = useMemo(() => {
		const id = monitor?.id ?? ""
		return monitorStats.map((record) =>
			record.stats?.[id] && record.stats[id].res_avg == null ? monitorGapRecord : record
		)
	}, [monitorStats, monitor?.id])

	const legend = dataPoints.length > 1

	return (
		<ChartCard
			legend={true}
			empty={empty}
			title={t`Response`}
			description={t`Average, minimum, and maximum response time`}
			grid={false}
		>
			<LineChartDefault
				truncate
				chartData={chartData}
				customData={data}
				dataPoints={dataPoints}
				domain={["auto", "auto"]}
				legend={legend}
				tickFormatter={(value) => formatMicroseconds(value, false)}
				contentFormatter={({ value }) => {
					if (typeof value !== "number") {
						return value
					}
					return formatMicroseconds(value)
				}}
			/>
		</ChartCard>
	)
}

export function LossChart({ monitorStats, grid, monitors, chartData, empty, titlePrefix }: MonitorChartProps) {
	const { t } = useLingui()
	const lossTitle = t({ message: "Loss", context: "Packet loss" })
	const title = titlePrefix ? `${titlePrefix} — ${lossTitle}` : lossTitle

	return (
		<MonitorChart
			monitorStats={monitorStats}
			grid={grid}
			monitors={monitors}
			chartData={chartData}
			empty={empty}
			metric="loss"
			title={title}
			description={t`Packet loss (%)`}
			domain={[0, 100]}
			color="var(--destructive)"
			tickFormatter={(value) => `${toFixedFloat(value, value >= 10 ? 0 : 1)}%`}
			contentFormatter={({ value }) => {
				if (typeof value !== "number") {
					return value
				}
				return `${decimalString(value, 2)}%`
			}}
		/>
	)
}

/** One point of the recent checks chart. */
type RecentCheckPoint = {
	/** Check time in milliseconds. */
	created: number
	/** Response time (ms) of successful and pending checks; null for failures. */
	res: number | null
	/** Marker of failed checks, at the response time or 0 without one. */
	failed: number | null
	/** Marker of pending (retrying) checks. */
	pending: number | null
	/** Raw response time in ms; -1 when the check failed. */
	ms: number
}

/** Chart points of a monitor's recent checks; exported for tests. */
export function recentCheckPoints(recent: MonitorRecentCheck[] | null | undefined): RecentCheckPoint[] {
	if (!recent?.length) {
		return []
	}
	return recent.map(([unixSec, state, ms]) => {
		const hasResponse = ms >= 0
		const isDown = state === 0 || !hasResponse
		return {
			created: unixSec * 1000,
			res: !isDown ? ms : null,
			failed: isDown ? (hasResponse ? ms : 0) : null,
			pending: state === 2 ? (hasResponse ? ms : 0) : null,
			ms,
		}
	})
}

const formatMs = (ms: number, fixedDigits = true) => formatMicroseconds(Math.round(ms * 1000), fixedDigits)

const markerDot =
	(fill: string) =>
	({ key, cx, cy, value }: { key?: string; cx?: number; cy?: number; value?: unknown }) => {
		if (value == null || cx == null || cy == null) {
			return <g key={key} />
		}
		return <circle key={key} cx={cx} cy={cy} r={3} fill={fill} stroke="var(--card)" strokeWidth={1} />
	}

const failedDot = markerDot("var(--color-red-500, #ef4444)")
const pendingDot = markerDot("var(--color-yellow-500, #eab308)")

/**
 * Response time of a monitor's latest checks (the `recent` field), with failed and pending
 * checks marked. Updates live with the monitor record, so it serves as the realtime chart of
 * hub and push monitors, whose checks aren't streamed by an agent.
 */
export function RecentChecksChart({ monitor, chartData }: { monitor: NetworkMonitorRecord; chartData: ChartData }) {
	const { t } = useLingui()
	const { yAxisWidth, updateYAxisWidth } = useYAxisWidth()
	const data = useMemo(() => recentCheckPoints(monitor.recent), [monitor.recent])
	const hasFailed = data.some((point) => point.failed != null)
	const hasPending = data.some((point) => point.pending != null)
	const responseLabel = t`Response`
	const failedLabel = t({ message: "Failed", comment: "Chart legend: failed monitor checks" })
	const pendingLabel = t({ message: "Pending", comment: "Chart legend: pending (retrying) monitor checks" })

	return (
		<ChartCard
			legend={true}
			empty={!data.length}
			title={t`Recent checks`}
			description={t`Response time of the latest ${data.length} checks`}
			grid={false}
		>
			{data.length > 0 && (
				<ChartContainer
					className={cn("h-full w-full absolute aspect-auto bg-card opacity-0 transition-opacity", {
						"opacity-100": yAxisWidth,
					})}
				>
					<LineChart accessibilityLayer data={data} margin={chartMargin}>
						<CartesianGrid vertical={false} />
						<YAxis
							direction="ltr"
							orientation={chartData.orientation}
							className="tracking-tighter"
							width={yAxisWidth}
							domain={[0, "auto"]}
							tickFormatter={(value) => updateYAxisWidth(formatMs(value, false))}
							tickLine={false}
							axisLine={false}
						/>
						<XAxis
							dataKey="created"
							type="number"
							scale="time"
							domain={["dataMin", "dataMax"]}
							minTickGap={12}
							tickMargin={8}
							axisLine={false}
							tickFormatter={hourWithSeconds}
						/>
						<ChartTooltip
							animationEasing="ease-out"
							animationDuration={150}
							content={
								<ChartTooltipContent
									labelFormatter={(_, payload) => formatShortDate(new Date(payload[0].payload.created).toISOString())}
									contentFormatter={(value: unknown) => {
										const item = value as { dataKey?: string; payload: RecentCheckPoint }
										const { ms } = item.payload
										if (item.dataKey !== "res" && ms < 0) {
											return "—"
										}
										return formatMs(ms)
									}}
								/>
							}
						/>
						<Line
							dataKey="res"
							name={responseLabel}
							type="monotoneX"
							stroke="var(--chart-1)"
							strokeWidth={1.5}
							dot={isolatedDot}
							isAnimationActive={false}
						/>
						{hasPending && (
							<Line
								dataKey="pending"
								name={pendingLabel}
								stroke="var(--color-yellow-500, #eab308)"
								strokeWidth={0}
								dot={pendingDot}
								activeDot={false}
								legendType="circle"
								isAnimationActive={false}
							/>
						)}
						{hasFailed && (
							<Line
								dataKey="failed"
								name={failedLabel}
								stroke="var(--color-red-500, #ef4444)"
								strokeWidth={0}
								dot={failedDot}
								activeDot={false}
								legendType="circle"
								isAnimationActive={false}
							/>
						)}
						<ChartLegend content={<ChartLegendContent />} />
					</LineChart>
				</ChartContainer>
			)}
		</ChartCard>
	)
}
