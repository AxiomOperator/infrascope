import { useCallback, useMemo, useRef, useState } from "react"
import { useStore } from "@nanostores/react"
import type { ChartConfig } from "@/components/ui/chart"
import type { ChartData, SystemStats, SystemStatsRecord } from "@/types"
import type { DataPoint } from "./area-chart"
import { $containerFilter } from "@/lib/stores"

/** Chart configurations for CPU, memory, and network usage charts */
export interface ContainerChartConfigs {
	cpu: ChartConfig
	memory: ChartConfig
	network: ChartConfig
}

/**
 * Generates chart configurations for container metrics visualization
 * @param containerData - Array of container statistics data points
 * @returns Chart configurations for CPU, memory, and network metrics
 */
export function useContainerChartConfigs(containerData: ChartData["containerData"]): ContainerChartConfigs {
	return useMemo(() => {
		const data = containerData ?? []
		const configs = {
			cpu: {} as ChartConfig,
			memory: {} as ChartConfig,
			network: {} as ChartConfig,
		}

		// Aggregate usage metrics for each container
		const totalUsage = {
			cpu: new Map<string, number>(),
			memory: new Map<string, number>(),
			network: new Map<string, number>(),
		}

		// Process each data point to calculate totals
		for (let i = 0; i < data.length; i++) {
			const stats = data[i]
			const containerNames = Object.keys(stats)

			for (let j = 0; j < containerNames.length; j++) {
				const containerName = containerNames[j]
				// Skip metadata field
				if (containerName === "created") {
					continue
				}

				const containerStats = stats[containerName]
				if (!containerStats) {
					continue
				}

				// Accumulate metrics for CPU, memory, and network
				const currentCpu = totalUsage.cpu.get(containerName) ?? 0
				const currentMemory = totalUsage.memory.get(containerName) ?? 0
				const currentNetwork = totalUsage.network.get(containerName) ?? 0
				const sentBytes = containerStats.b?.[0] ?? (containerStats.ns ?? 0) * 1024 * 1024
				const recvBytes = containerStats.b?.[1] ?? (containerStats.nr ?? 0) * 1024 * 1024

				totalUsage.cpu.set(containerName, currentCpu + (containerStats.c ?? 0))
				totalUsage.memory.set(containerName, currentMemory + (containerStats.m ?? 0))
				totalUsage.network.set(containerName, currentNetwork + sentBytes + recvBytes)
			}
		}

		// Generate chart configurations for each metric type
		Object.entries(totalUsage).forEach(([chartType, usageMap]) => {
			const sortedContainers = Array.from(usageMap.entries()).sort(([, a], [, b]) => b - a)
			const chartConfig = {} as Record<string, { label: string; color: string }>
			const count = sortedContainers.length

			// Generate colors for each container
			for (let i = 0; i < count; i++) {
				const [containerName] = sortedContainers[i]
				const hue = ((i * 360) / count) % 360
				chartConfig[containerName] = {
					label: containerName,
					color: `hsl(${hue}, var(--chart-saturation), var(--chart-lightness))`,
				}
			}

			configs[chartType as keyof typeof configs] = chartConfig
		})

		return configs
	}, [containerData])
}

/** Horizontal space around the widest tick label (tick margin + breathing room), in px */
const Y_AXIS_LABEL_PADDING = 20
/** Upper bound for cached label widths before the cache is reset */
const LABEL_WIDTH_CACHE_LIMIT = 2000

let labelMeasureContext: CanvasRenderingContext2D | null | undefined
const labelWidthCache = new Map<string, number>()

if (typeof document !== "undefined") {
	// widths measured with a fallback font are wrong once the real font arrives
	document.fonts?.addEventListener?.("loadingdone", () => labelWidthCache.clear())
}

/**
 * Measures the rendered width of a y axis tick label (text-xs, tabular-nums, tracking-tighter)
 * using a shared canvas context. Results are cached per label.
 */
function measureTickLabel(label: string): number {
	const cached = labelWidthCache.get(label)
	if (cached !== undefined) {
		return cached
	}
	if (labelMeasureContext === undefined) {
		labelMeasureContext = typeof document === "undefined" ? null : document.createElement("canvas").getContext("2d")
	}
	const ctx = labelMeasureContext
	let width: number
	if (ctx) {
		const rootFontSize = Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
		const fontSize = rootFontSize * 0.75 // text-xs
		ctx.font = `${fontSize}px ${getComputedStyle(document.body).fontFamily || "sans-serif"}`
		if ("letterSpacing" in ctx) {
			ctx.letterSpacing = `${fontSize * -0.05}px` // tracking-tighter
		}
		// canvas can't enable tabular-nums, so measure every digit as "0" (a full-width figure)
		width = ctx.measureText(label.replace(/\d/g, "0")).width
	} else {
		// no canvas (e.g. tests): rough estimate for a 12px font
		width = label.length * 7
	}
	width = Math.ceil(width)
	if (labelWidthCache.size >= LABEL_WIDTH_CACHE_LIMIT) {
		labelWidthCache.clear()
	}
	labelWidthCache.set(label, width)
	return width
}

/**
 * Sets the correct width of the y axis in recharts based on the longest label.
 *
 * Pass tick labels through `updateYAxisWidth` from the axis `tickFormatter`. All labels formatted
 * in one render pass are collected and the width is set to fit the widest of them once the pass
 * is done, so the axis grows and shrinks with its labels.
 */
export function useYAxisWidth() {
	const [yAxisWidth, setYAxisWidth] = useState(0)
	const pass = useRef({ max: 0, scheduled: false })

	const updateYAxisWidth = useCallback((str: string) => {
		const current = pass.current
		current.max = Math.max(current.max, measureTickLabel(str) + Y_AXIS_LABEL_PADDING)
		if (!current.scheduled) {
			current.scheduled = true
			// tick formatters run while recharts renders, so commit the result after the pass finishes
			setTimeout(() => {
				const width = current.max
				current.max = 0
				current.scheduled = false
				setYAxisWidth(width)
			})
		}
		return str
	}, [])

	return { yAxisWidth, updateYAxisWidth }
}

/** Subscribes to the container filter store and returns filtered DataPoints for container charts */
export function useContainerDataPoints(
	chartConfig: ChartConfig,
	// biome-ignore lint/suspicious/noExplicitAny: container data records have dynamic keys
	dataFn: (key: string, data: Record<string, any>) => number | null
) {
	const filter = useStore($containerFilter)
	const { dataPoints, filteredKeys } = useMemo(() => {
		const filterTerms = filter
			? filter
					.toLowerCase()
					.split(" ")
					.filter((term) => term.length > 0)
			: []
		const filtered = new Set<string>()
		const points = Object.keys(chartConfig).map((key) => {
			const isFiltered = filterTerms.length > 0 && !filterTerms.some((term) => key.toLowerCase().includes(term))
			if (isFiltered) filtered.add(key)
			return {
				label: key,
				// biome-ignore lint/suspicious/noExplicitAny: container data records have dynamic keys
				dataKey: (data: Record<string, any>) => dataFn(key, data),
				color: chartConfig[key].color ?? "",
				opacity: isFiltered ? 0.05 : 0.4,
				strokeOpacity: isFiltered ? 0.1 : 1,
				activeDot: !isFiltered,
				stackId: "a",
			}
		})
		return {
			// biome-ignore lint/suspicious/noExplicitAny: container data records have dynamic keys
			dataPoints: points as DataPoint<Record<string, any>>[],
			filteredKeys: filtered,
		}
	}, [chartConfig, filter])
	return { filter, dataPoints, filteredKeys }
}

// Assures consistent colors for network interfaces
export function useNetworkInterfaces(interfaces: SystemStats["ni"]) {
	const keys = Object.keys(interfaces ?? {})
	const sortedKeys = keys.sort((a, b) => (interfaces?.[b]?.[3] ?? 0) - (interfaces?.[a]?.[3] ?? 0))
	return {
		length: sortedKeys.length,
		data: (index = 3) => {
			return sortedKeys.map((key) => ({
				label: key,
				dataKey: ({ stats }: SystemStatsRecord) => stats?.ni?.[key]?.[index],
				color: `hsl(${220 + (((sortedKeys.indexOf(key) * 360) / sortedKeys.length) % 360)}, 70%, 50%)`,

				opacity: 0.3,
			}))
		},
	}
}
