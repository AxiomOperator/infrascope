import { expect, test } from "bun:test"
import { isDockerManaged, isManagedField, managedContainer } from "./docker-discovery"

test("isDockerManaged", () => {
	expect(isDockerManaged(undefined)).toBe(false)
	expect(isDockerManaged({ managedBy: "" })).toBe(false)
	expect(isDockerManaged({ managedBy: "docker" })).toBe(true)
})

test("managedContainer", () => {
	expect(managedContainer({ managedBy: "docker", managedKey: "docker:web:default" })).toBe("web")
	expect(managedContainer({ managedBy: "docker", managedKey: "docker:proxy:traefik.app.a.example.com" })).toBe("proxy")
	expect(managedContainer({ managedBy: "", managedKey: "docker:web:default" })).toBe("")
	expect(managedContainer({ managedBy: "docker" })).toBe("")
})

test("isManagedField", () => {
	const monitor = { managedBy: "docker", managedFields: ["target", "keyword"] }
	expect(isManagedField(monitor, "target")).toBe(true)
	expect(isManagedField(monitor, "keyword")).toBe(true)
	expect(isManagedField(monitor, "name")).toBe(false)
	expect(isManagedField({ managedFields: ["target"] }, "target")).toBe(false)
	expect(isManagedField(null, "target")).toBe(false)
})
