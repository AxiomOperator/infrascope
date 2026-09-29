import { useCallback, useRef, useState } from "react"
import { Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { ChevronDownIcon, SearchIcon, ServerIcon } from "lucide-react"
import {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { HUB_LOCATION } from "@/lib/monitor-locations"
import { $systems } from "@/lib/stores"
import { cn, supportsNetworkMonitors } from "@/lib/utils"

/**
 * Multi-select of the systems that can run monitors. With includeHub, the hub is offered
 * first as the "hub" location, and keepIds keeps systems listed although they cannot run
 * monitors (for example the current locations of a monitor being edited).
 */
export function SystemMultiSelect({
	id,
	selectedSystemIds,
	onChange,
	disabled,
	className,
	includeHub = false,
	keepIds,
	placeholder,
}: {
	id: string
	selectedSystemIds: Set<string>
	onChange: (ids: Set<string>) => void
	disabled?: boolean
	className?: string
	includeHub?: boolean
	keepIds?: string[]
	placeholder?: string
}) {
	const allSystems = useStore($systems)
	const { t } = useLingui()
	const hubName = t`Hub`
	const systems = includeHub
		? [{ id: HUB_LOCATION, name: hubName }, ...allSystems.filter(supportsSystem)]
		: allSystems.filter(supportsSystem)
	function supportsSystem(system: (typeof allSystems)[number]) {
		return supportsNetworkMonitors(system) || !!keepIds?.includes(system.id)
	}
	const [search, setSearch] = useState("")
	const searchRef = useRef<HTMLInputElement>(null)
	const focusSearchOnMount = useCallback((node: HTMLInputElement | null) => {
		searchRef.current = node
		if (!node) return
		// Focus after the menu has completed its own initial focus handling.
		const frame = requestAnimationFrame(() => node.focus())
		return () => cancelAnimationFrame(frame)
	}, [])
	const contentRef = useRef<HTMLDivElement>(null)
	const query = search.trim().toLocaleLowerCase()
	const filteredSystems = systems.filter((system) => system.name.toLocaleLowerCase().includes(query))
	const allSelected = filteredSystems.every((system) => selectedSystemIds.has(system.id))
	const anySelected = filteredSystems.some((system) => selectedSystemIds.has(system.id))

	const selectFiltered = (selected: boolean) => {
		const next = new Set(selectedSystemIds)
		for (const system of filteredSystems) {
			if (selected) next.add(system.id)
			else next.delete(system.id)
		}
		onChange(next)
	}
	return (
		<DropdownMenu onOpenChange={() => setSearch("")}>
			<DropdownMenuTrigger asChild>
				<Button
					id={id}
					disabled={disabled}
					type="button"
					variant="outline"
					className={cn("relative w-full min-w-0 ps-10 pe-10 justify-start font-normal text-start", className)}
				>
					<ServerIcon className="size-3.5 absolute start-4 top-1/2 -translate-y-1/2 opacity-85" />
					<span className="truncate">
						{selectedSystemIds.size === 0
							? (placeholder ?? t`Select systems`)
							: selectedSystemIds.size === 1
								? systems.find((s) => selectedSystemIds.has(s.id))?.name
								: t`${selectedSystemIds.size} selected`}
					</span>
					<ChevronDownIcon className="size-4 absolute end-4 top-1/2 -translate-y-1/2 opacity-50" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				ref={contentRef}
				onKeyDown={(event) => {
					if (event.key === "Tab") {
						event.preventDefault()
						searchRef.current?.focus()
					}
				}}
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
							placeholder={t`Search systems`}
							aria-label={t`Search systems`}
							className="h-10 min-w-0 rounded-none border-0 bg-transparent px-0 shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
							onKeyDown={(event) => {
								if (event.key === "Escape") return
								// Keep menu typeahead and form submission from consuming search input.
								event.stopPropagation()
								if (event.key === "Enter") event.preventDefault()
								if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Tab") {
									event.preventDefault()
									const items = contentRef.current?.querySelectorAll<HTMLElement>(
										'[role^="menuitem"]:not([data-disabled])'
									)
									const index = event.key === "ArrowUp" || event.shiftKey ? (items?.length ?? 1) - 1 : 0
									items?.[index]?.focus()
								}
							}}
						/>
					</div>
					<div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-1 pb-1">
						<div className="flex items-center">
							<DropdownMenuItem
								className="px-1.5 py-1 text-xs text-muted-foreground"
								disabled={!filteredSystems.length || allSelected}
								onSelect={(event) => {
									event.preventDefault()
									selectFiltered(true)
								}}
							>
								{query ? <Trans>Select matches</Trans> : <Trans>Select all</Trans>}
							</DropdownMenuItem>
							<span aria-hidden="true" className="text-xs text-muted-foreground/50">
								·
							</span>
							<DropdownMenuItem
								className="px-1.5 py-1 text-xs text-muted-foreground"
								disabled={!anySelected}
								onSelect={(event) => {
									event.preventDefault()
									selectFiltered(false)
								}}
							>
								{query ? <Trans>Clear matches</Trans> : <Trans>Clear all</Trans>}
							</DropdownMenuItem>
						</div>
						<span className="px-1.5 text-xs tabular-nums text-muted-foreground">
							{t`${selectedSystemIds.size} selected`}
						</span>
					</div>
				</div>
				<div className="min-h-0 overflow-y-auto">
					{filteredSystems.length === 0 && (
						<output className="block px-2.5 py-3 text-sm text-muted-foreground">
							<Trans>No systems found.</Trans>
						</output>
					)}
					{filteredSystems.map((sys) => (
						<DropdownMenuCheckboxItem
							key={sys.id}
							checked={selectedSystemIds.has(sys.id)}
							onSelect={(event) => event.preventDefault()}
							onCheckedChange={(checked) => {
								const next = new Set(selectedSystemIds)
								if (checked) next.add(sys.id)
								else next.delete(sys.id)
								onChange(next)
							}}
							className="group min-w-0 gap-2.5 py-2 ps-2.5"
							indicatorClassName="static size-4 shrink-0 rounded border border-input group-data-[state=checked]:border-primary group-data-[state=checked]:bg-primary group-data-[state=checked]:text-primary-foreground [&_svg]:size-3"
						>
							<span className="truncate">{sys.name}</span>
						</DropdownMenuCheckboxItem>
					))}
				</div>
			</DropdownMenuContent>
		</DropdownMenu>
	)
}
