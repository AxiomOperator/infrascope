import { Trans, useLingui } from "@lingui/react/macro"
import { UnplugIcon } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { isUnreachable, parseSuppressedBy } from "@/lib/monitor-dependencies"
import { cn } from "@/lib/utils"

/**
 * Badge of a monitor or system whose notifications are suppressed because a monitor it depends on
 * is down: "Unreachable", with the down parents in a tooltip. Renders nothing otherwise.
 */
export function UnreachableBadge({
	record,
	className,
}: {
	record: { status?: string; suppressedBy?: string | null; enabled?: boolean }
	className?: string
}) {
	const { t } = useLingui()
	if (!isUnreachable(record)) {
		return null
	}
	const parents = parseSuppressedBy(record.suppressedBy).join(", ")
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge
					variant="outline"
					className={cn(
						"relative z-10 gap-1 border-dashed border-muted-foreground/50 bg-muted/60 text-muted-foreground font-medium pointer-events-auto",
						className
					)}
					aria-label={t`Unreachable — parent ${parents} down`}
				>
					<UnplugIcon className="size-3" />
					<Trans>Unreachable</Trans>
				</Badge>
			</TooltipTrigger>
			<TooltipContent className="max-w-80">
				<p>
					<Trans>Unreachable — parent {parents} down</Trans>
				</p>
				<p className="text-muted-foreground text-xs mt-1">
					<Trans>Notifications are suppressed until the parent recovers.</Trans>
				</p>
			</TooltipContent>
		</Tooltip>
	)
}
