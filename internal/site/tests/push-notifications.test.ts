import { describe, expect, test } from "bun:test"
import {
	defaultDeviceName,
	pushSupport,
	sameKey,
	serviceWorkerLocation,
	urlBase64ToUint8Array,
} from "../src/lib/push-notifications"

describe("urlBase64ToUint8Array", () => {
	test("decodes base64url without padding", () => {
		expect(Array.from(urlBase64ToUint8Array("AQID"))).toEqual([1, 2, 3])
		expect(Array.from(urlBase64ToUint8Array("-_8"))).toEqual([251, 255])
		expect(Array.from(urlBase64ToUint8Array("AQ"))).toEqual([1])
		expect(Array.from(urlBase64ToUint8Array(""))).toEqual([])
	})

	test("decodes a VAPID public key to an uncompressed P-256 point", () => {
		const key = urlBase64ToUint8Array(
			"BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
		)
		expect(key.length).toBe(65)
		expect(key[0]).toBe(4)
	})
})

describe("sameKey", () => {
	test("compares bytes", () => {
		const key = new Uint8Array([1, 2, 3])
		expect(sameKey(new Uint8Array([1, 2, 3]).buffer, key)).toBe(true)
		expect(sameKey(new Uint8Array([1, 2, 4]), key)).toBe(false)
		expect(sameKey(new Uint8Array([1, 2]), key)).toBe(false)
		expect(sameKey(null, key)).toBe(false)
	})
})

describe("pushSupport", () => {
	const all = { hasServiceWorker: true, hasPushManager: true, hasNotification: true }
	test("requires a secure context", () => {
		expect(pushSupport({ ...all, isSecureContext: false })).toBe("insecure")
		expect(pushSupport({ ...all, isSecureContext: true })).toBe("supported")
	})
	test("requires service workers, push and notifications", () => {
		expect(pushSupport({ ...all, isSecureContext: true, hasPushManager: false })).toBe("unsupported")
		expect(pushSupport({ ...all, isSecureContext: true, hasServiceWorker: false })).toBe("unsupported")
		expect(pushSupport({ ...all, isSecureContext: true, hasNotification: false })).toBe("unsupported")
	})
})

describe("defaultDeviceName", () => {
	test("names browser and OS", () => {
		expect(defaultDeviceName("Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")).toBe(
			"Firefox on Linux"
		)
		expect(
			defaultDeviceName(
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"
			)
		).toBe("Edge on Windows")
		expect(
			defaultDeviceName(
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
			)
		).toBe("Safari on macOS")
		expect(
			defaultDeviceName(
				"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
			)
		).toBe("Safari on iOS")
		expect(
			defaultDeviceName(
				"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36"
			)
		).toBe("Chrome on Android")
		expect(defaultDeviceName("")).toBe("Browser")
	})
})

describe("serviceWorkerLocation", () => {
	test("places the worker at the base path root", () => {
		expect(serviceWorkerLocation("")).toEqual({ url: "/sw.js", scope: "/" })
		expect(serviceWorkerLocation("/")).toEqual({ url: "/sw.js", scope: "/" })
		expect(serviceWorkerLocation("/hub/")).toEqual({ url: "/hub/sw.js", scope: "/hub/" })
		expect(serviceWorkerLocation("/a/b")).toEqual({ url: "/a/b/sw.js", scope: "/a/b/" })
	})
})
