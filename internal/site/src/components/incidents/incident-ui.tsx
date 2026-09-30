import { t } from "@lingui/core/macro"
import { Badge } from "@/components/ui/badge"
import { linkify } from "@/lib/incidents"
import { cn, formatShortDate } from "@/lib/utils"
import type { IncidentImpact, IncidentStatus } from "@/types"

export function incidentStatusLabel(status: IncidentStatus) {
	switch (status) {
		case "investigating":
			return t`Investigating`
		case "identified":
			return t`Identified`
		case "monitoring":
			return t`Monitoring`
		case "resolved":
			return t`Resolved`
	}
	return status
}

export function incidentImpactLabel(impact: IncidentImpact) {
	switch (impact) {
		case "none":
			return t`No impact`
		case "minor":
			return t`Minor impact`
		case "major":
			return t`Major impact`
		case "critical":
			return t`Critical impact`
	}
	return impact
}

/** Tailwind classes of the border and background of an incident card by impact. */
export const incidentImpactCardColors: Record<IncidentImpact, string> = {
	none: "border-border bg-card",
	minor: "border-amber-500/30 bg-amber-500/5",
	major: "border-orange-500/40 bg-orange-500/5",
	critical: "border-red-500/40 bg-red-500/5",
}

const statusBadgeColors: Record<IncidentStatus, string> = {
	investigating: "bg-red-500/15! text-red-600 dark:text-red-400",
	identified: "bg-orange-500/15! text-orange-600 dark:text-orange-400",
	monitoring: "bg-blue-500/15! text-blue-600 dark:text-blue-400",
	resolved: "bg-green-500/15! text-green-700 dark:text-green-400",
}

const impactBadgeColors: Record<IncidentImpact, string> = {
	none: "bg-muted! text-muted-foreground",
	minor: "bg-amber-500/15! text-amber-600 dark:text-amber-400",
	major: "bg-orange-500/15! text-orange-600 dark:text-orange-400",
	critical: "bg-red-500/15! text-red-600 dark:text-red-400",
}

export function IncidentStatusBadge({ status, className }: { status: IncidentStatus; className?: string }) {
	return (
		<Badge className={cn("shrink-0", statusBadgeColors[status] ?? statusBadgeColors.investigating, className)}>
			{incidentStatusLabel(status)}
		</Badge>
	)
}

export function IncidentImpactBadge({ impact, className }: { impact: IncidentImpact; className?: string }) {
	return (
		<Badge className={cn("shrink-0", impactBadgeColors[impact] ?? impactBadgeColors.none, className)}>
			{incidentImpactLabel(impact)}
		</Badge>
	)
}

/**
 * Plain text with preserved line breaks and http(s) URLs as links. The text is
 * rendered as text nodes, never as HTML.
 */
export function IncidentText({ text, className }: { text: string; className?: string }) {
	return (
		<p className={cn("whitespace-pre-line break-words", className)}>
			{linkify(text).map((segment, i) =>
				segment.type === "link" ? (
					<a
						key={i}
						href={segment.href}
						target="_blank"
						rel="noopener noreferrer nofollow"
						className="underline underline-offset-2 hover:text-foreground break-all"
					>
						{segment.value}
					</a>
				) : (
					<span key={i}>{segment.value}</span>
				)
			)}
		</p>
	)
}

export interface TimelineUpdate {
	id?: string
	status: IncidentStatus
	message: string
	created: string
}

/** The updates of an incident, newest first. */
export function IncidentTimeline({ updates, className }: { updates: TimelineUpdate[]; className?: string }) {
	if (updates.length === 0) return null
	return (
		<ol className={cn("grid gap-3 border-s ps-4 ms-1", className)}>
			{updates.map((update, i) => (
				<li key={update.id ?? `${update.created}-${i}`} className="relative grid gap-1">
					<span
						aria-hidden="true"
						className="absolute -start-[1.3rem] top-1.5 size-2 rounded-full bg-muted-foreground/60 ring-2 ring-background"
					/>
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
						<span className="font-medium">{incidentStatusLabel(update.status)}</span>
						<time className="text-xs text-muted-foreground tabular-nums" dateTime={update.created}>
							{formatShortDate(update.created)}
						</time>
					</div>
					<IncidentText text={update.message} className="text-sm text-muted-foreground" />
				</li>
			))}
		</ol>
	)
}
