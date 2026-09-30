import { expect, test } from "bun:test"
import { dependencyCandidates, dependentIds, isUnreachable, parseSuppressedBy } from "./monitor-dependencies"

test("parseSuppressedBy splits parent names", () => {
	expect(parseSuppressedBy("")).toEqual([])
	expect(parseSuppressedBy(undefined)).toEqual([])
	expect(parseSuppressedBy("Router")).toEqual(["Router"])
	expect(parseSuppressedBy("Router, Core switch")).toEqual(["Router", "Core switch"])
})

test("isUnreachable needs a down parent and a record that is not up", () => {
	expect(isUnreachable({ status: "down", suppressedBy: "Router" })).toBe(true)
	expect(isUnreachable({ status: "pending", suppressedBy: "Router" })).toBe(true)
	expect(isUnreachable({ status: "down", suppressedBy: "" })).toBe(false)
	expect(isUnreachable({ status: "up", suppressedBy: "Router" })).toBe(false)
	expect(isUnreachable({ status: "paused", suppressedBy: "Router" })).toBe(false)
	expect(isUnreachable({ status: "down", suppressedBy: "Router", enabled: false })).toBe(false)
})

const monitors = [
	{ id: "router", dependsOn: [] },
	{ id: "switch", dependsOn: ["router"] },
	{ id: "host", dependsOn: ["switch"] },
	{ id: "web", dependsOn: ["host", "router"] },
	{ id: "other", dependsOn: null },
]

test("dependentIds follows chains", () => {
	expect([...dependentIds(monitors, "router")].sort()).toEqual(["host", "switch", "web"])
	expect([...dependentIds(monitors, "host")]).toEqual(["web"])
	expect(dependentIds(monitors, "other").size).toBe(0)
})

test("dependencyCandidates excludes itself and its dependents", () => {
	expect(dependencyCandidates(monitors, "switch").map((m) => m.id)).toEqual(["router", "other"])
	expect(dependencyCandidates(monitors).length).toBe(monitors.length)
	// Cycles in stored data do not loop forever.
	const cyclic = [
		{ id: "a", dependsOn: ["b"] },
		{ id: "b", dependsOn: ["a"] },
	]
	expect(dependencyCandidates(cyclic, "a")).toEqual([])
})
