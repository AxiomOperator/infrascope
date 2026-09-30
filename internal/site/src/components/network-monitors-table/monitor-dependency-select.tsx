import { useCallback, useEffect, useRef, useState } from "react"
import { Trans, useLingui } from "@lingui/react/macro"
import { ChevronDownIcon, NetworkIcon, SearchIcon } from "lucide-react"
import {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { pb } from "@/lib/api"
import { dependencyCandidates, MAX_DEPENDENCIES } from "@/lib/monitor-dependencies"
import { getMonitorName, monitorStatusBgColors } from "@/lib/network-monitor-utils"
import { cn } from "@/lib/utils"
import type { NetworkMonitorRecord } from "@/types"

export type DependencyMonitor = Pick<
	NetworkMonitorRecord,
	"id" | "name" | "target" | "protocol" | "port" | "status" | "enabled" | "dependsOn"
>

const DEPENDENCY_FIELDS = "id,name,target,protocol,port,status,enabled,dependsOn"

/** Monitors the user can view, for choosing and showing dependencies. */
export function useDependencyMonitors(enabled = true) {
	const [monitors, setMonitors] = useState<DependencyMonitor[]>([])
	useEffect(() => {
		if (!enabled) return
		let cancelled = false
		pb.collection<DependencyMonitor>("network_monitors")
			.getFullList({ fields: DEPENDENCY_FIELDS, sort: "name", requestKey: null })
			.then((records) => {
				if (!cancelled) setMonitors(records)
			})
			.catch((error) => console.error("Failed to load monitors", error))
		return () => {
			cancelled = true
		}
	}, [enabled])
	return monitors
}

/**
 * Multi-select of the monitors a monitor or system depends on (at most MAX_DEPENDENCIES). For a
 * monitor, monitorId excludes itself and the monitors depending on it, which would form a cycle.
 */
export function MonitorDependencySelect({
	id,
	value,
	onChange,
	monitorId,
	disabled,
}: {
	id: string
	value: string[]
	onChange: (ids: string[]) => void
	monitorId?: string
	disabled?: boolean
}) {
	const { t } = useLingui()
	const monitors = useDependencyMonitors()
	const candidates = dependencyCandidates(monitors, monitorId)
	const [search, setSearch] = useState("")
	const contentRef = useRef<HTMLDivElement>(null)
	const focusSearchOnMount = useCallback((node: HTMLInputElement | null) => {
		if (!node) return
		const frame = requestAnimationFrame(() => node.focus())
		return () => cancelAnimationFrame(frame)
	}, [])
	const query = search.trim().toLocaleLowerCase()
	const filtered = candidates.filter((monitor) => getMonitorName(monitor).toLocaleLowerCase().includes(query))
	const selected = new Set(value)
	const full = value.length >= MAX_DEPENDENCIES
	const nameOf = (monitorId: string) => {
		const monitor = monitors.find((candidate) => candidate.id === monitorId)
		return monitor ? getMonitorName(monitor) : monitorId
	}
	const label =
		value.length === 0 ? t`No dependencies` : value.length === 1 ? nameOf(value[0]) : t`${value.length} selected`

	return (
		<DropdownMenu onOpenChange={() => setSearch("")}>
			<DropdownMenuTrigger asChild>
				<Button
					id={id}
					disabled={disabled}
					type="button"
					variant="outline"
					className="relative w-full min-w-0 ps-10 pe-10 justify-start font-normal text-start"
				>
					<NetworkIcon className="size-3.5 absolute start-4 top-1/2 -translate-y-1/2 opacity-85" />
					<span className="truncate">{label}</span>
					<ChevronDownIcon className="size-4 absolute end-4 top-1/2 -translate-y-1/2 opacity-50" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				ref={contentRef}
				align="start"
				className="w-[var(--radix-dropdown-menu-trigger-width)] max-h-[min(20rem,var(--radix-dropdown-menu-content-available-height))] flex flex-col overflow-hidden"
			>
				<div className="shrink-0 border-b mb-1">
					<div className="flex items-center gap-2 px-2.5">
						<SearchIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
						<Input
							ref={focusSearchOnMount}
							value={search}
							onChange={(event) => setSearch(event.target.value)}
							placeholder={t`Search monitors`}
							aria-label={t`Search monitors`}
							className="h-10 min-w-0 rounded-none border-0 bg-transparent px-0 shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
							onKeyDown={(event) => {
								if (event.key === "Escape") return
								// Keep menu typeahead and form submission from consuming search input.
								event.stopPropagation()
								if (event.key === "Enter") event.preventDefault()
								if (event.key === "ArrowDown" || event.key === "ArrowUp") {
									event.preventDefault()
									const items = contentRef.current?.querySelectorAll<HTMLElement>(
										'[role^="menuitem"]:not([data-disabled])'
									)
									items?.[event.key === "ArrowUp" ? (items?.length ?? 1) - 1 : 0]?.focus()
								}
							}}
						/>
					</div>
					<p className="px-2.5 pb-1.5 text-xs tabular-nums text-muted-foreground">
						<Trans>
							{value.length} of {MAX_DEPENDENCIES} selected
						</Trans>
					</p>
				</div>
				<div className="min-h-0 overflow-y-auto">
					{filtered.length === 0 && (
						<output className="block px-2.5 py-3 text-sm text-muted-foreground">
							<Trans>No monitors found.</Trans>
						</output>
					)}
					{filtered.map((monitor) => {
						const checked = selected.has(monitor.id)
						return (
							<DropdownMenuCheckboxItem
								key={monitor.id}
								checked={checked}
								disabled={!checked && full}
								onSelect={(event) => event.preventDefault()}
								onCheckedChange={(next) => {
									onChange(next ? [...value, monitor.id] : value.filter((id) => id !== monitor.id))
								}}
								className="group min-w-0 gap-2.5 py-2 ps-2.5"
								indicatorClassName="static size-4 shrink-0 rounded border border-input group-data-[state=checked]:border-primary group-data-[state=checked]:bg-primary group-data-[state=checked]:text-primary-foreground [&_svg]:size-3"
							>
								<span
									className={cn(
										"size-2 shrink-0 rounded-full",
										monitorStatusBgColors[monitor.enabled === false ? "paused" : monitor.status]
									)}
								/>
								<span className="truncate">{getMonitorName(monitor)}</span>
							</DropdownMenuCheckboxItem>
						)
					})}
				</div>
			</DropdownMenuContent>
		</DropdownMenu>
	)
}
