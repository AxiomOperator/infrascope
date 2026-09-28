import { useEffect, useState } from "react"
import { pb } from "@/lib/api"

/**
 * Loads all records of a user-owned collection and keeps them in sync through a
 * realtime subscription. New records are prepended; the initial list uses `sort`.
 */
export function useOwnedRecords<T extends { id: string }>(collection: string, sort: string) {
	const [records, setRecords] = useState<T[]>([])
	const [loading, setLoading] = useState(true)

	useEffect(() => {
		let cancelled = false
		let unsubscribe: (() => void) | undefined

		pb.collection<T>(collection)
			.getFullList({ sort })
			.then((items) => {
				if (!cancelled) setRecords(items)
			})
			.catch((error) => {
				if (!cancelled) console.error(`Failed to load ${collection}`, error)
			})
			.finally(() => {
				if (!cancelled) setLoading(false)
			})
		;(async () => {
			try {
				const unsub = await pb.collection<T>(collection).subscribe("*", (e) => {
					if (cancelled) return
					const record = e.record
					if (e.action === "delete") {
						setRecords((current) => current.filter((r) => r.id !== record.id))
						return
					}
					setRecords((current) =>
						current.some((r) => r.id === record.id)
							? current.map((r) => (r.id === record.id ? record : r))
							: [record, ...current]
					)
				})
				// the component may have unmounted while subscribing
				if (cancelled) unsub()
				else unsubscribe = unsub
			} catch (error) {
				console.error(`Failed to subscribe to ${collection}`, error)
			}
		})()

		return () => {
			cancelled = true
			unsubscribe?.()
		}
	}, [collection, sort])

	return { records, loading }
}
