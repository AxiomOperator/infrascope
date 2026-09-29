import { expect, test } from "bun:test"
import {
	countLocationStatuses,
	defaultQuorum,
	effectiveQuorum,
	getLocationName,
	getLocationStatuses,
	getMonitorLocations,
	isMultiLocation,
	locationStatsSystem,
	locationSystemIds,
	orderLocations,
	primaryLocationSystem,
} from "./monitor-locations"

test("locations of legacy and multi-location monitors", () => {
	expect(getMonitorLocations({ system: "" })).toEqual(["hub"])
	expect(getMonitorLocations({ system: "a", locations: null })).toEqual(["a"])
	expect(getMonitorLocations({ system: "a", locations: [] })).toEqual(["a"])
	expect(getMonitorLocations({ system: "a", locations: ["hub", "a", "a", "b"] })).toEqual(["hub", "a", "b"])
	expect(isMultiLocation({ system: "a", locations: ["a"] })).toBe(false)
	expect(isMultiLocation({ system: "a", locations: ["hub", "a"] })).toBe(true)
})

test("quorum defaults to a majority", () => {
	expect([1, 2, 3, 4, 5].map(defaultQuorum)).toEqual([1, 2, 2, 3, 3])
	expect(effectiveQuorum(0, 3)).toBe(2)
	expect(effectiveQuorum(1, 3)).toBe(1)
	expect(effectiveQuorum(4, 3)).toBe(2)
	expect(effectiveQuorum(undefined, 1)).toBe(1)
})

test("primary and systems of locations", () => {
	expect(primaryLocationSystem(["hub", "b", "a"])).toBe("b")
	expect(primaryLocationSystem(["hub"])).toBe("")
	expect(locationSystemIds(["hub", "b", "a"])).toEqual(["b", "a"])
	expect(locationStatsSystem("hub")).toBe("")
	expect(locationStatsSystem("a")).toBe("a")
	expect(orderLocations(["b", "hub", "a", "b"])).toEqual(["hub", "b", "a"])
	expect(orderLocations(new Set(["b", "a"]))).toEqual(["b", "a"])
})

test("location names", () => {
	const systems = { a: { name: "alpha" } }
	expect(getLocationName("hub", systems, "Hub")).toBe("Hub")
	expect(getLocationName("a", systems, "Hub")).toBe("alpha")
	expect(getLocationName("gone", systems, "Hub")).toBe("gone")
})

test("location statuses", () => {
	const states = getLocationStatuses({
		system: "a",
		locations: ["hub", "a", "b"],
		enabled: true,
		locationStatus: { hub: { status: "up", res: 1200 }, a: { status: "down", lastError: "timeout" } },
	})
	expect(states).toEqual([
		{ location: "hub", status: "up", res: 1200 },
		{ location: "a", status: "down", lastError: "timeout" },
		{ location: "b", status: "unknown" },
	])
	expect(countLocationStatuses(states)).toEqual({ up: 1, down: 1, unknown: 1 })
	expect(getLocationStatuses({ system: "", enabled: false, locationStatus: null })).toEqual([
		{ location: "hub", status: "paused" },
	])
})
