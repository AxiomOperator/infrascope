import { memo } from "react"
import { copyToClipboard, getHubURL } from "@/lib/utils"
import { DropdownMenuContent, DropdownMenuItem } from "./ui/dropdown-menu"

const SCRIPTS_BASE_URL = "https://raw.githubusercontent.com/AxiomOperator/infrascope/main/supplemental/scripts"
const AGENT_IMAGE = "ghcr.io/axiomoperator/infrascope/beszel-agent"

/**
 * Get the URL of an install script in the InfraScope repository.
 * @param name - The script file name (e.g. "install-agent.sh").
 * @returns The URL for the script.
 */
const getScriptUrl = (name: string) => `${SCRIPTS_BASE_URL}/${name}`

export function copyDockerCompose(port = "45876", publicKey: string, token: string) {
	copyToClipboard(`services:
  beszel-agent:
    image: ${AGENT_IMAGE}
    container_name: beszel-agent
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./beszel_agent_data:/var/lib/beszel-agent
      # monitor other disks / partitions by mounting a folder in /extra-filesystems
      # - /mnt/disk/.beszel:/extra-filesystems/sda1:ro
    environment:
      LISTEN: ${port}
      KEY: '${publicKey}'
      TOKEN: ${token}
      HUB_URL: ${getHubURL()}`)
}

export function copyDockerRun(port = "45876", publicKey: string, token: string) {
	copyToClipboard(
		`docker run -d --name beszel-agent --network host --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock:ro -v beszel_agent_data:/var/lib/beszel-agent -e KEY="${publicKey}" -e LISTEN=${port} -e TOKEN="${token}" -e HUB_URL="${getHubURL()}" ${AGENT_IMAGE}`
	)
}

export function copyLinuxCommand(port = "45876", publicKey: string, token: string) {
	copyToClipboard(
		`curl -sL ${getScriptUrl("install-agent.sh")} -o /tmp/install-agent.sh && chmod +x /tmp/install-agent.sh && /tmp/install-agent.sh -p ${port} -k "${publicKey}" -t "${token}" -url "${getHubURL()}"`
	)
}

/**
 * Not offered in the UI yet: install-agent.ps1 still installs the agent from the upstream
 * Beszel scoop bucket / winget package rather than InfraScope releases.
 */
export function copyWindowsCommand(port = "45876", publicKey: string, token: string) {
	copyToClipboard(
		`& iwr -useb ${getScriptUrl("install-agent.ps1")} -OutFile "$env:TEMP\\install-agent.ps1"; & Powershell -ExecutionPolicy Bypass -File "$env:TEMP\\install-agent.ps1" -Key "${publicKey}" -Port ${port} -Token "${token}" -Url "${getHubURL()}"`
	)
}

export interface DropdownItem {
	text: string
	onClick?: () => void
	url?: string
	icons?: React.ComponentType<React.SVGProps<SVGSVGElement>>[]
}

export const InstallDropdown = memo(({ items }: { items: DropdownItem[] }) => {
	return (
		<DropdownMenuContent align="end">
			{items.map((item, index) => {
				const className = "cursor-pointer flex items-center gap-1.5"
				return item.url ? (
					<DropdownMenuItem key={index} asChild>
						<a href={item.url} className={className} target="_blank" rel="noopener noreferrer">
							{item.text}{" "}
							{item.icons?.map((Icon, iconIndex) => (
								<Icon key={iconIndex} className="size-4" />
							))}
						</a>
					</DropdownMenuItem>
				) : (
					<DropdownMenuItem key={index} onClick={item.onClick} className={className}>
						{item.text}{" "}
						{item.icons?.map((Icon, iconIndex) => (
							<Icon key={iconIndex} className="size-4" />
						))}
					</DropdownMenuItem>
				)
			})}
		</DropdownMenuContent>
	)
})
