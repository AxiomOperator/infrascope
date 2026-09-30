import type { IncidentImpact, IncidentRecord, IncidentStatus } from "@/types"

export const INCIDENT_STATUSES: IncidentStatus[] = ["investigating", "identified", "monitoring", "resolved"]
export const INCIDENT_IMPACTS: IncidentImpact[] = ["none", "minor", "major", "critical"]
/** Same as incident_updates.message. */
export const MAX_INCIDENT_MESSAGE = 5000
/** Same as incidents.title. */
export const MAX_INCIDENT_TITLE = 200

export type TextSegment = { type: "text"; value: string } | { type: "link"; value: string; href: string }

const URL_PATTERN = /\bhttps?:\/\/[^\s<>"']+/gi
const TRAILING_PUNCTUATION = /[.,;:!?'"]+$/

/** Trim punctuation that ends a sentence rather than the URL, keeping balanced parentheses. */
function trimUrl(url: string) {
	let result = url.replace(TRAILING_PUNCTUATION, "")
	while (result.endsWith(")") && (result.match(/\(/g)?.length ?? 0) < (result.match(/\)/g)?.length ?? 0)) {
		result = result.slice(0, -1).replace(TRAILING_PUNCTUATION, "")
	}
	return result
}

/** Returns the href of an http(s) URL, or null. */
export function safeHref(value: string): string | null {
	try {
		const url = new URL(value)
		return url.protocol === "http:" || url.protocol === "https:" ? url.href : null
	} catch {
		return null
	}
}

/**
 * Split plain text into text and http(s) link segments. Text is never parsed as
 * HTML; callers render segments as text nodes and anchors.
 */
export function linkify(text: string): TextSegment[] {
	const segments: TextSegment[] = []
	let last = 0
	const push = (value: string) => {
		if (!value) return
		const previous = segments[segments.length - 1]
		if (previous?.type === "text") previous.value += value
		else segments.push({ type: "text", value })
	}
	for (const match of text.matchAll(URL_PATTERN)) {
		const start = match.index ?? 0
		const url = trimUrl(match[0])
		const href = safeHref(url)
		push(text.slice(last, start))
		if (href) {
			segments.push({ type: "link", value: url, href })
		} else {
			push(url)
		}
		last = start + url.length
	}
	push(text.slice(last))
	return segments
}

function time(value: string | undefined) {
	const ms = value ? new Date(value).getTime() : Number.NaN
	return Number.isNaN(ms) ? 0 : ms
}

/** Split incidents into active ones (most recently started first) and resolved ones (most recently resolved first). */
export function splitIncidents<T extends Pick<IncidentRecord, "status" | "startedAt" | "resolvedAt" | "created">>(
	records: T[]
) {
	const active: T[] = []
	const resolved: T[] = []
	for (const record of records) {
		if (record.status === "resolved") resolved.push(record)
		else active.push(record)
	}
	active.sort((a, b) => time(b.startedAt || b.created) - time(a.startedAt || a.created))
	resolved.sort((a, b) => time(b.resolvedAt) - time(a.resolvedAt))
	return { active, resolved }
}

/** Format a duration in milliseconds as e.g. "2d 3h", "1h 5m" or "42m" (at least "1m"). */
export function formatIncidentDuration(ms: number) {
	const minutes = Math.max(1, Math.round(ms / 60_000))
	const days = Math.floor(minutes / 1440)
	const hours = Math.floor((minutes % 1440) / 60)
	const mins = minutes % 60
	if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`
	if (hours > 0) return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`
	return `${mins}m`
}
