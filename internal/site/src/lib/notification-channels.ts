/** Pure helpers for notification channels, alert severities and templates. */
import type { AlertSeverity, NotificationChannelRecord, NotificationChannelType, NotificationTemplate } from "@/types"

/** Severities from lowest to highest. */
export const SEVERITIES: readonly AlertSeverity[] = ["info", "warning", "critical"] as const

/** Maximum length of a template title or body. */
export const TEMPLATE_MAX_CHARS = 2000

/** Maximum length of a channel name. */
export const CHANNEL_NAME_MAX_CHARS = 100

/** Variables available in notification templates, in display order. */
export const TEMPLATE_VARIABLES = [
	"Title",
	"Message",
	"Severity",
	"Status",
	"Name",
	"Value",
	"Link",
	"AckLink",
	"Time",
] as const

export type TemplateVariable = (typeof TEMPLATE_VARIABLES)[number]

/** Functions available in notification templates. */
export const TEMPLATE_FUNCS = ["upper", "lower", "title", "trim", "default", "truncate"] as const

export function isSeverity(value: unknown): value is AlertSeverity {
	return typeof value === "string" && (SEVERITIES as readonly string[]).includes(value)
}

/** Rank of a severity (info 0, warning 1, critical 2); unknown values rank as info. */
export function severityRank(severity: string | undefined | null): number {
	const index = SEVERITIES.indexOf(severity as AlertSeverity)
	return index < 0 ? 0 : index
}

/** Days left at or below which a certificate expiry alert is critical. */
export const CERT_CRITICAL_DAYS = 3

const criticalAlerts = new Set(["Status", "MonitorDown", "ContainerHealth", "SystemdFailed"])

/**
 * Default severity of an alert type. value is the days left for certificate alerts.
 * Down, unhealthy and failed alerts are critical; everything else is a warning.
 */
export function defaultSeverity(alertName: string, value?: number): AlertSeverity {
	if (criticalAlerts.has(alertName)) return "critical"
	if (alertName === "MonitorCert" && value !== undefined && value <= CERT_CRITICAL_DAYS) return "critical"
	return "warning"
}

/** Severity of an alert or history row: its stored severity, else the type's default. */
export function effectiveSeverity(record: { name: string; severity?: string | null; val?: number; value?: number }) {
	if (isSeverity(record.severity)) return record.severity
	return defaultSeverity(record.name, record.val ?? record.value)
}

type RoutableChannel = Pick<NotificationChannelRecord, "id" | "enabled" | "isDefault" | "minSeverity">

/** Whether a default channel receives alerts of a severity. */
export function channelAcceptsSeverity(channel: Pick<RoutableChannel, "minSeverity">, severity: AlertSeverity) {
	return severityRank(channel.minSeverity) <= severityRank(severity)
}

/**
 * Channels an alert of a severity is sent to: its enabled explicit channels when any of
 * them exist, else every enabled default channel whose minimum severity it reaches.
 */
export function routeAlert<T extends RoutableChannel>(
	channels: T[],
	severity: AlertSeverity,
	explicit: string[] = []
): T[] {
	const selected = explicit.length ? channels.filter((c) => explicit.includes(c.id)) : []
	if (selected.length) return selected.filter((c) => c.enabled)
	return channels.filter((c) => c.enabled && c.isDefault && channelAcceptsSeverity(c, severity))
}

/** A channel to create, without server-managed fields. */
export interface ChannelDraft {
	name: string
	type: NotificationChannelType
	config: { addresses?: string[]; url?: string }
	enabled: boolean
	minSeverity: AlertSeverity
	isDefault: boolean
	template: NotificationTemplate | null
}

/** Defaults of a new channel of a type. */
export function newChannelDraft(type: NotificationChannelType, name = ""): ChannelDraft {
	return {
		name,
		type,
		config: type === "email" ? { addresses: [] } : type === "shoutrrr" ? { url: "" } : {},
		enabled: true,
		minSeverity: "info",
		isDefault: true,
		template: null,
	}
}

/**
 * Channels equivalent to legacy emails and webhooks settings: one "Email" channel with all
 * addresses and one channel per webhook, named by nameOf. All are enabled defaults receiving
 * every severity, so alerts reach the same destinations as before.
 */
export function legacyChannelDrafts(
	settings: { emails?: string[]; webhooks?: string[] },
	nameOf: (url: string) => string
): ChannelDraft[] {
	const drafts: ChannelDraft[] = []
	const emails = (settings.emails ?? []).map((e) => e.trim()).filter(Boolean)
	if (emails.length) {
		drafts.push({ ...newChannelDraft("email", "Email"), config: { addresses: emails } })
	}
	const webhooks = (settings.webhooks ?? []).map((w) => w.trim()).filter(Boolean)
	const counts = new Map<string, number>()
	for (const url of webhooks) {
		const base = (nameOf(url) || "Webhook").slice(0, CHANNEL_NAME_MAX_CHARS - 4)
		const n = (counts.get(base) ?? 0) + 1
		counts.set(base, n)
		drafts.push({ ...newChannelDraft("shoutrrr", n > 1 ? `${base} ${n}` : base), config: { url } })
	}
	return drafts
}

/** Whether legacy settings still hold destinations that are used while no channels exist. */
export function hasLegacyDestinations(settings: { emails?: string[]; webhooks?: string[] }) {
	return (settings.emails ?? []).some((e) => e.trim()) || (settings.webhooks ?? []).some((w) => w.trim())
}

/** A template with blank fields removed; null when empty. */
export function normalizeTemplate(template: NotificationTemplate | null | undefined): NotificationTemplate | null {
	const title = template?.title?.trim() ? template.title : undefined
	const body = template?.body?.trim() ? template.body : undefined
	if (!title && !body) return null
	return { ...(title ? { title } : {}), ...(body ? { body } : {}) }
}

/** Client-side template check: the fields that exceed TEMPLATE_MAX_CHARS. */
export function templateTooLong(template: NotificationTemplate | null | undefined): ("title" | "body")[] {
	const fields: ("title" | "body")[] = []
	if ((template?.title?.length ?? 0) > TEMPLATE_MAX_CHARS) fields.push("title")
	if ((template?.body?.length ?? 0) > TEMPLATE_MAX_CHARS) fields.push("body")
	return fields
}

/** Validation problems of a channel draft, as keys for the form. */
export function validateChannelDraft(draft: ChannelDraft): Partial<Record<"name" | "config" | "template", true>> {
	const errors: Partial<Record<"name" | "config" | "template", true>> = {}
	const name = draft.name.trim()
	if (!name || name.length > CHANNEL_NAME_MAX_CHARS) errors.name = true
	if (draft.type === "email") {
		const addresses = draft.config.addresses ?? []
		if (!addresses.length || !addresses.every(isEmailAddress)) errors.config = true
	} else if (draft.type === "shoutrrr") {
		if (!isUrl(draft.config.url ?? "")) errors.config = true
	}
	if (templateTooLong(draft.template).length) errors.template = true
	return errors
}

/** The payload stored for a channel draft: config keeps only the fields of its type. */
export function channelPayload(draft: ChannelDraft) {
	const config =
		draft.type === "email"
			? { addresses: draft.config.addresses ?? [] }
			: draft.type === "shoutrrr"
				? { url: (draft.config.url ?? "").trim() }
				: {}
	return {
		name: draft.name.trim(),
		type: draft.type,
		config,
		enabled: draft.enabled,
		minSeverity: draft.minSeverity,
		isDefault: draft.isDefault,
		template: normalizeTemplate(draft.template),
	}
}

/** Draft of a stored channel, for editing. */
export function draftFromChannel(channel: NotificationChannelRecord): ChannelDraft {
	return {
		name: channel.name,
		type: channel.type,
		config: {
			addresses: channel.config?.addresses ?? [],
			url: channel.config?.url ?? "",
		},
		enabled: channel.enabled,
		minSeverity: isSeverity(channel.minSeverity) ? channel.minSeverity : "info",
		isDefault: channel.isDefault,
		template: normalizeTemplate(channel.template),
	}
}

/** Sorts channels: default channels first, then by name. */
export function sortChannels<T extends Pick<NotificationChannelRecord, "name" | "isDefault">>(channels: T[]): T[] {
	return [...channels].sort(
		(a, b) => Number(b.isDefault) - Number(a.isDefault) || a.name.localeCompare(b.name, undefined, { numeric: true })
	)
}

export function isEmailAddress(value: string) {
	return /^[^\s@]+@[^\s@]+$/.test(value.trim())
}

function isUrl(value: string) {
	try {
		const url = new URL(value.trim())
		return !!url.protocol && url.protocol !== ":"
	} catch {
		return false
	}
}
