import { expect, test } from "bun:test"
import {
	filterUnacknowledged,
	indexOpenHistory,
	isAcknowledged,
	needsAcknowledgement,
	openHistoryKey,
	parseReminderMinutes,
	takeAckParam,
} from "./alert-ack"

test("acknowledgement state", () => {
	expect(isAcknowledged({ acknowledgedAt: "2026-01-01 00:00:00.000Z" })).toBe(true)
	expect(isAcknowledged({ acknowledgedAt: "" })).toBe(false)
	expect(needsAcknowledgement({ acknowledgedAt: "", resolved: "" })).toBe(true)
	expect(needsAcknowledgement({ acknowledgedAt: "x", resolved: "" })).toBe(false)
	expect(needsAcknowledgement({ acknowledgedAt: "", resolved: "x" })).toBe(false)
})

test("filterUnacknowledged keeps open unacknowledged rows", () => {
	const rows = [
		{ id: "a", acknowledgedAt: "", resolved: "" },
		{ id: "b", acknowledgedAt: "x", resolved: "" },
		{ id: "c", acknowledgedAt: "", resolved: "x" },
	]
	expect(filterUnacknowledged(rows, false)).toBe(rows)
	expect(filterUnacknowledged(rows, true).map((r) => r.id)).toEqual(["a"])
})

test("parseReminderMinutes", () => {
	expect(parseReminderMinutes("")).toBe(0)
	expect(parseReminderMinutes(undefined)).toBe(0)
	expect(parseReminderMinutes("0")).toBe(0)
	expect(parseReminderMinutes("5")).toBe(5)
	expect(parseReminderMinutes(" 30 ")).toBe(30)
	expect(parseReminderMinutes(1440)).toBe(1440)
	expect(parseReminderMinutes("4")).toBeNull()
	expect(parseReminderMinutes("1441")).toBeNull()
	expect(parseReminderMinutes("7.5")).toBeNull()
	expect(parseReminderMinutes("-5")).toBeNull()
	expect(parseReminderMinutes("abc")).toBeNull()
})

test("takeAckParam reads and removes the ack parameter", () => {
	expect(takeAckParam("https://hub.test/system/abc?ack=1")).toEqual({
		acknowledged: true,
		url: "https://hub.test/system/abc",
	})
	expect(takeAckParam("https://hub.test/?x=2&ack=1#top")).toEqual({
		acknowledged: true,
		url: "https://hub.test/?x=2#top",
	})
	expect(takeAckParam("https://hub.test/?ack=nope")).toEqual({ acknowledged: false, url: "https://hub.test/" })
	expect(takeAckParam("https://hub.test/monitors")).toEqual({
		acknowledged: false,
		url: "https://hub.test/monitors",
	})
})

test("indexOpenHistory maps system and monitor alerts", () => {
	const rows = [
		{ id: "1", alert_id: "alert1", name: "CPU", resolved: "" },
		{ id: "2", alert_id: "mon1", monitor: "mon1", name: "MonitorDown", resolved: "" },
		{ id: "3", alert_id: "alert2", name: "CPU", resolved: "2026-01-01" },
	]
	const index = indexOpenHistory(rows)
	expect(index.get(openHistoryKey("alert1"))?.id).toBe("1")
	expect(index.get(openHistoryKey("mon1", "MonitorDown"))?.id).toBe("2")
	expect(index.has(openHistoryKey("alert2"))).toBe(false)
	expect(index.size).toBe(2)
})
