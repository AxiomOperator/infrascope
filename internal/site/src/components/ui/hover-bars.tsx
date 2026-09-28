import { type ReactNode, useState } from "react"
import { createPortal } from "react-dom"
import { cn } from "@/lib/utils"

type HoverState = { index: number; rect: DOMRect }

/**
 * A row of thin vertical bars with a tooltip for the hovered bar. Built from
 * plain elements and a single shared tooltip so many rows stay cheap to render.
 * Empty placeholder bars pad the start when there are fewer items than slots,
 * so the newest item is always on the right.
 */
export function HoverBars<T>({
	items,
	slots = items.length,
	getBarClassName,
	renderTooltip,
	label,
	className,
	barClassName,
}: {
	items: T[]
	slots?: number
	getBarClassName: (item: T) => string
	renderTooltip: (item: T) => ReactNode
	label?: string
	className?: string
	barClassName?: string
}) {
	const [hover, setHover] = useState<HoverState | null>(null)
	const shown = items.length > slots ? items.slice(items.length - slots) : items
	const placeholders = Math.max(0, slots - shown.length)
	const hovered = hover ? shown[hover.index] : undefined

	return (
		<div
			role="img"
			aria-label={label}
			className={cn("flex items-stretch gap-px h-4", className)}
			onPointerMove={(event) => {
				const target = event.target as HTMLElement
				const index = target.dataset.index
				if (index === undefined) {
					return
				}
				const nextIndex = Number(index)
				if (hover?.index !== nextIndex) {
					setHover({ index: nextIndex, rect: target.getBoundingClientRect() })
				}
			}}
			onPointerLeave={() => setHover(null)}
		>
			{placeholders > 0 &&
				Array.from({ length: placeholders }, (_, i) => (
					<span key={`p${i}`} className={cn("flex-1 min-w-0.5 rounded-[1px] bg-muted", barClassName)} />
				))}
			{shown.map((item, i) => (
				<span
					key={i}
					data-index={i}
					className={cn(
						"flex-1 min-w-0.5 rounded-[1px] transition-opacity hover:opacity-70",
						getBarClassName(item),
						barClassName
					)}
				/>
			))}
			{hover && hovered !== undefined && <BarTooltip rect={hover.rect}>{renderTooltip(hovered)}</BarTooltip>}
		</div>
	)
}

function BarTooltip({ rect, children }: { rect: DOMRect; children: ReactNode }) {
	const below = rect.top < 90
	return createPortal(
		<div
			role="tooltip"
			className="pointer-events-none fixed z-[100] w-max max-w-72 rounded-md border bg-popover px-2.5 py-1.5 text-xs text-popover-foreground shadow-md"
			style={{
				left: rect.left + rect.width / 2,
				top: below ? rect.bottom + 6 : rect.top - 6,
				transform: below ? "translateX(-50%)" : "translate(-50%, -100%)",
			}}
		>
			{children}
		</div>,
		document.body
	)
}
