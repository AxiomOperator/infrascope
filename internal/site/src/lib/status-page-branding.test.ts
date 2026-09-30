import { expect, test } from "bun:test"
import {
	accentVariables,
	badgeSnippets,
	badgeUrl,
	contrastRatio,
	getCustomDomainSlug,
	groupsProblem,
	isValidCustomDomain,
	layoutComponents,
	moveItem,
	normalizeCustomDomain,
	pruneGroups,
	readableForeground,
	readableOn,
} from "./status-page-branding"

test("readableForeground picks black or white", () => {
	expect(readableForeground("#ffffff")).toBe("#000000")
	expect(readableForeground("#ffeb3b")).toBe("#000000")
	expect(readableForeground("#000000")).toBe("#ffffff")
	expect(readableForeground("#1d4ed8")).toBe("#ffffff")
})

test("readableOn reaches the contrast on light and dark backgrounds", () => {
	for (const color of ["#ffeb3b", "#22c55e", "#1d4ed8", "#000000", "#ffffff", "#ab12cd"]) {
		expect(contrastRatio(readableOn(color, "#ffffff"), "#ffffff")).toBeGreaterThanOrEqual(4.5)
		expect(contrastRatio(readableOn(color, "#161718"), "#161718")).toBeGreaterThanOrEqual(4.5)
	}
	// readable colors are kept
	expect(readableOn("#1D4ED8", "#ffffff")).toBe("#1d4ed8")
	expect(readableOn("nope", "#ffffff")).toBe("nope")
})

test("accentVariables", () => {
	expect(accentVariables("", false)).toBeUndefined()
	expect(accentVariables("red", false)).toBeUndefined()
	const vars = accentVariables("#FFEB3B", false)
	expect(vars?.["--accent-brand"]).toBe("#ffeb3b")
	expect(vars?.["--accent-brand-fg"]).toBe("#000000")
	expect(vars?.["--accent-brand-text"]).not.toBe("#ffeb3b")
	expect(accentVariables("#ffeb3b", true)?.["--accent-brand-text"]).toBe("#ffeb3b")
})

test("custom domains", () => {
	expect(normalizeCustomDomain("  Status.Example.COM. ")).toBe("status.example.com")
	expect(isValidCustomDomain("")).toBe(true)
	expect(isValidCustomDomain("Status.Example.com")).toBe(true)
	for (const invalid of [
		"localhost",
		"https://a.example.com",
		"a.example.com:80",
		"a.example.com/x",
		"10.0.0.1",
		"a_b.example.com",
	]) {
		expect(isValidCustomDomain(invalid)).toBe(false)
	}
})

const day = { d: "2026-01-01", up: null, st: "none" as const }
const uptime = { d1: null, d7: null, d30: null }
const system = (name: string) => ({ name, status: "up" as const, uptime, days: [day] })
const monitor = (name: string) => ({ name, status: "up" as const, uptime, days: [day] })

test("layoutComponents splits grouped and ungrouped components", () => {
	const layout = layoutComponents({
		systems: [system("s0"), system("s1")],
		monitors: [monitor("m0"), monitor("m1"), monitor("m2")],
		groups: [
			{
				name: "A",
				collapsed: true,
				status: "up",
				items: [
					{ kind: "monitor", index: 2 },
					{ kind: "system", index: 1 },
					{ kind: "monitor", index: 9 },
				],
			},
			{ name: "Empty", collapsed: false, status: "up", items: [{ kind: "system", index: 5 }] },
		],
	})
	expect(layout.groups).toHaveLength(1)
	expect(layout.groups[0].items.map((i) => i.item.name)).toEqual(["m2", "s1"])
	expect(layout.systems).toEqual([0])
	expect(layout.monitors).toEqual([0, 1])
	expect(layoutComponents({ systems: [], monitors: [monitor("m")] })).toEqual({
		groups: [],
		systems: [],
		monitors: [0],
	})
})

test("pruneGroups removes components not on the page and duplicates", () => {
	const groups = pruneGroups(
		[
			{
				name: "A",
				components: [
					{ type: "monitor", id: "m1" },
					{ type: "monitor", id: "gone" },
					{ type: "system", id: "m1" },
				],
			},
			{
				name: "B",
				components: [
					{ type: "monitor", id: "m1" },
					{ type: "system", id: "s1" },
				],
			},
		],
		["m1"],
		["s1"]
	)
	expect(groups[0].components).toEqual([{ type: "monitor", id: "m1" }])
	expect(groups[1].components).toEqual([{ type: "system", id: "s1" }])
})

test("moveItem and groupsProblem", () => {
	expect(moveItem([1, 2, 3], 0, 1)).toEqual([2, 1, 3])
	expect(moveItem([1, 2, 3], 2, -2)).toEqual([3, 1, 2])
	expect(moveItem([1, 2, 3], 2, 1)).toEqual([1, 2, 3])
	expect(groupsProblem([{ name: "ok", components: [] }])).toBeNull()
	expect(groupsProblem([{ name: " ", components: [] }])).toBe("name")
	expect(groupsProblem([{ name: "x".repeat(101), components: [] }])).toBe("name")
	expect(groupsProblem(Array.from({ length: 51 }, () => ({ name: "g", components: [] })))).toBe("count")
})

test("badge URLs and snippets", () => {
	expect(badgeUrl("https://hub.example", "my", { kind: "page" })).toBe(
		"https://hub.example/api/beszel/status-pages/my/badge.svg"
	)
	expect(
		badgeUrl("", "my", { kind: "monitor", position: 2 }, { type: "uptime", period: "30d", label: " A & B " })
	).toBe("/api/beszel/status-pages/my/badge/monitor/2.svg?type=uptime&period=30d&label=A+%26+B")
	expect(badgeUrl("", "my", { kind: "system", position: 1 }, { type: "status", period: "7d" })).toBe(
		"/api/beszel/status-pages/my/badge/system/1.svg"
	)
	const snippets = badgeSnippets("https://x/b.svg?a=1&b=2", "https://x/status/my", 'Web "API" [prod]')
	expect(snippets.markdown).toBe('[![Web "API" \\[prod\\]](https://x/b.svg?a=1&b=2)](https://x/status/my)')
	expect(snippets.html).toBe(
		'<a href="https://x/status/my"><img src="https://x/b.svg?a=1&#38;b=2" alt="Web &#34;API&#34; [prod]" /></a>'
	)
})

test("getCustomDomainSlug reads the meta tag", () => {
	const doc = (content: string | null) => ({
		querySelector: () => (content === null ? null : { getAttribute: () => content }),
	})
	expect(getCustomDomainSlug(doc("my-page") as never)).toBe("my-page")
	expect(getCustomDomainSlug(doc(null) as never)).toBeNull()
	expect(getCustomDomainSlug(doc("../x") as never)).toBeNull()
	expect(getCustomDomainSlug(undefined)).toBeNull()
})
