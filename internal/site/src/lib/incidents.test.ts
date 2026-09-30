import { expect, test } from "bun:test"
import { formatIncidentDuration, linkify, safeHref, splitIncidents } from "./incidents"

test("linkify keeps plain text as text", () => {
	expect(linkify("")).toEqual([])
	expect(linkify("All good\nsecond line")).toEqual([{ type: "text", value: "All good\nsecond line" }])
	expect(linkify("<b>not html</b>")).toEqual([{ type: "text", value: "<b>not html</b>" }])
})

test("linkify links http(s) URLs and trims trailing punctuation", () => {
	expect(linkify("See https://example.com/status. Thanks")).toEqual([
		{ type: "text", value: "See " },
		{ type: "link", value: "https://example.com/status", href: "https://example.com/status" },
		{ type: "text", value: ". Thanks" },
	])
	expect(linkify("(details: http://a.example/x_(y))")).toEqual([
		{ type: "text", value: "(details: " },
		{ type: "link", value: "http://a.example/x_(y)", href: "http://a.example/x_(y)" },
		{ type: "text", value: ")" },
	])
	expect(linkify("https://a.example, https://b.example")).toEqual([
		{ type: "link", value: "https://a.example", href: "https://a.example/" },
		{ type: "text", value: ", " },
		{ type: "link", value: "https://b.example", href: "https://b.example/" },
	])
})

test("linkify never links other schemes or quotes", () => {
	for (const text of ["javascript:alert(1)", "data:text/html,<script>", "ftp://example.com", "mailto:a@b.c"]) {
		expect(linkify(text)).toEqual([{ type: "text", value: text }])
	}
	const [, link] = linkify('x https://example.com/"onmouseover="alert(1)')
	expect(link).toEqual({ type: "link", value: "https://example.com/", href: "https://example.com/" })
})

test("safeHref accepts only http and https", () => {
	expect(safeHref("https://example.com")).toBe("https://example.com/")
	expect(safeHref("javascript:alert(1)")).toBeNull()
	expect(safeHref("not a url")).toBeNull()
})

test("splitIncidents orders active by start and resolved by resolution", () => {
	const records = [
		{ id: "a", status: "investigating", startedAt: "2026-01-01 10:00:00.000Z", resolvedAt: "", created: "" },
		{
			id: "b",
			status: "resolved",
			startedAt: "2026-01-01 09:00:00.000Z",
			resolvedAt: "2026-01-02 00:00:00.000Z",
			created: "",
		},
		{ id: "c", status: "monitoring", startedAt: "2026-01-03 10:00:00.000Z", resolvedAt: "", created: "" },
		{
			id: "d",
			status: "resolved",
			startedAt: "2026-01-01 08:00:00.000Z",
			resolvedAt: "2026-01-05 00:00:00.000Z",
			created: "",
		},
	] as const
	const { active, resolved } = splitIncidents([...records])
	expect(active.map((r) => r.id)).toEqual(["c", "a"])
	expect(resolved.map((r) => r.id)).toEqual(["d", "b"])
})

test("formatIncidentDuration", () => {
	expect(formatIncidentDuration(0)).toBe("1m")
	expect(formatIncidentDuration(42 * 60_000)).toBe("42m")
	expect(formatIncidentDuration(65 * 60_000)).toBe("1h 5m")
	expect(formatIncidentDuration(120 * 60_000)).toBe("2h")
	expect(formatIncidentDuration((2 * 1440 + 180) * 60_000)).toBe("2d 3h")
})
