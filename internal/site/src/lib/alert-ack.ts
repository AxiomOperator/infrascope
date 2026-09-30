/** Helpers for alert acknowledgement, notes and reminders. */

/** Fields of an alert history row that acknowledgement reads. */
export interface AckFields {
	acknowledgedAt?: string | null
	resolved?: string | null
}

/** Reminder interval bounds in minutes; 0 turns reminders off. */
export const REMINDER_MIN_MINUTES = 5
export const REMINDER_MAX_MINUTES = 1440
/** Maximum length of an acknowledgement note or alert note. */
export const ACK_NOTE_MAX_CHARS = 1000

/** Whether a history row has been acknowledged. */
export function isAcknowledged(record: AckFields): boolean {
	return !!record.acknowledgedAt
}

/** Whether a history row is open (unresolved) and not acknowledged. */
export function needsAcknowledgement(record: AckFields): boolean {
	return !record.resolved && !record.acknowledgedAt
}

/** Rows kept by the "unacknowledged only" filter: open rows that nobody acknowledged. */
export function filterUnacknowledged<T extends AckFields>(records: T[], unacknowledgedOnly: boolean): T[] {
	return unacknowledgedOnly ? records.filter(needsAcknowledgement) : records
}

/**
 * Parses the reminder interval input. Returns the minutes (0 for off, including
 * an empty input), or null when the value is not allowed.
 */
export function parseReminderMinutes(input: string | number | undefined | null): number | null {
	if (input === undefined || input === null) return 0
	const text = String(input).trim()
	if (text === "") return 0
	if (!/^\d+$/.test(text)) return null
	const minutes = Number(text)
	if (minutes === 0) return 0
	return minutes >= REMINDER_MIN_MINUTES && minutes <= REMINDER_MAX_MINUTES ? minutes : null
}

/**
 * Reads the "ack" query parameter set by the acknowledgement link redirect.
 * Returns whether it was present and the URL without it.
 */
export function takeAckParam(href: string): { acknowledged: boolean; url: string } {
	const url = new URL(href)
	if (!url.searchParams.has("ack")) {
		return { acknowledged: false, url: href }
	}
	const acknowledged = url.searchParams.get("ack") === "1"
	url.searchParams.delete("ack")
	return { acknowledged, url: url.toString() }
}

/** Key of an open history row for a system alert (alerts record id) or a monitor alert (monitor id + name). */
export function openHistoryKey(alertId: string, name?: string): string {
	return name ? `${alertId}:${name}` : alertId
}

/** Indexes open history rows by alerts record id and by "monitor:name" for monitor alerts. */
export function indexOpenHistory<
	T extends { alert_id?: string; monitor?: string; name: string; resolved?: string | null },
>(records: T[]): Map<string, T> {
	const index = new Map<string, T>()
	for (const record of records) {
		if (record.resolved) continue
		if (record.monitor) {
			index.set(openHistoryKey(record.monitor, record.name), record)
		} else if (record.alert_id) {
			index.set(openHistoryKey(record.alert_id), record)
		}
	}
	return index
}
