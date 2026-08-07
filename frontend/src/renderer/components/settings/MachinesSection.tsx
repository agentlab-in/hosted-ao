import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Check, Copy, Laptop, Loader2, Server } from "lucide-react";
import { useState } from "react";
import { aoBridge } from "../../lib/bridge";
import { formatLastSeen, type AoMachine, type AoMachinesState } from "../../../shared/ao-machines";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { SettingsSection } from "./SettingsSection";

export const aoMachinesQueryKey = ["ao-machines"] as const;

/**
 * The machine picker. One machine is active at a time, and switching re-points
 * the app at that machine's base URL.
 *
 * This computer is machine zero: it is always in the list, it is always
 * selectable, and it never needs an account. Everything below it is a machine
 * registered with `ao setup-vm`, which is the only way one gets here. No URL
 * and no token is ever typed into this app.
 */
export function MachinesSection() {
	const queryClient = useQueryClient();
	const query = useQuery({ queryKey: aoMachinesQueryKey, queryFn: () => aoBridge.machines.refresh() });
	const apply = (next: AoMachinesState) => queryClient.setQueryData(aoMachinesQueryKey, next);

	const select = useMutation({
		mutationFn: (machineId: string) => aoBridge.machines.select(machineId),
		onSuccess: apply,
	});

	const state = query.data;
	const machines = state?.machines ?? [];
	const activeMachineId = state?.activeMachineId ?? "";
	const mutationError = select.error instanceof Error ? select.error.message : null;
	const error = state?.error ?? mutationError;

	return (
		<SettingsSection title="Machines" sectionId="machines">
			{machines.map((machine) => (
				<MachineRow
					key={machine.id}
					machine={machine}
					active={machine.id === activeMachineId}
					busy={select.isPending}
					onSelect={() => select.mutate(machine.id)}
				/>
			))}

			{query.isLoading ? (
				<p className="flex items-center gap-2 px-1 text-xs leading-row text-settings-muted">
					<Loader2 className="size-icon-sm animate-spin" aria-hidden="true" />
					Looking for machines registered to your account…
				</p>
			) : null}

			<p className="px-1 text-xs leading-row text-settings-muted">
				{state?.status === "signed-out"
					? "Sign in to reach a machine you registered with `ao setup-vm`. This computer works without an account."
					: "One machine is active at a time. Switching points this app at that machine."}
			</p>

			{/* `ao doctor` is a local command with no HTTP route yet, so nothing
			    carries a registered machine's readiness here. Saying so once beats
			    a badge on every machine that would only mean "not asked". */}
			{machines.some((machine) => !machine.local && machine.harness === "unknown") ? (
				<p className="px-1 text-xs leading-row text-settings-muted" data-testid="ao-machines-harness-unknown">
					Agent-harness readiness is not reported for remote machines yet. Run{" "}
					<code className="font-mono">ao doctor</code> on the machine to check it.
				</p>
			) : null}

			{error ? (
				<p className="flex items-start gap-2 px-1 text-xs leading-row text-error" data-testid="ao-machines-error">
					<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
					<span>{error}</span>
				</p>
			) : null}
		</SettingsSection>
	);
}

function MachineRow({
	machine,
	active,
	busy,
	onSelect,
}: {
	machine: AoMachine;
	active: boolean;
	busy: boolean;
	onSelect: () => void;
}) {
	const Icon = machine.local ? Laptop : Server;
	const offline = machine.reachability === "offline";
	const lastSeen = formatLastSeen(machine.lastSeen);
	const detail = machine.local
		? "Runs on this computer"
		: offline
			? lastSeen
				? `Offline, last seen ${lastSeen}`
				: "Offline, has never connected"
			: new URL(machine.baseUrl).host;

	return (
		<div className="flex w-full flex-col gap-1.5" data-testid="ao-machine" data-machine-id={machine.id}>
			<button
				type="button"
				onClick={onSelect}
				disabled={busy || active}
				aria-pressed={active}
				className="settings-row-bar w-full text-left transition-colors hover:bg-settings-menu-selected disabled:hover:bg-transparent"
			>
				<div className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap)">
					<Icon className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" />
					<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{machine.name}</span>
				</div>
				<div className="flex min-w-0 flex-1 items-center justify-end gap-2">
					<span className="min-w-0 truncate text-control text-settings-muted">{detail}</span>
					{offline ? <Badge variant="error">Offline</Badge> : null}
					{machine.harness === "missing" ? <Badge variant="warning">No harness</Badge> : null}
					{active ? (
						<Badge variant="accent">
							<Check className="size-3" aria-hidden="true" />
							Active
						</Badge>
					) : null}
				</div>
			</button>

			{machine.harness === "missing" && machine.harnessCommand ? (
				<HarnessHint machineName={machine.name} command={machine.harnessCommand} />
			) : null}
		</div>
	);
}

/**
 * A registered machine with no agent harness cannot run an agent, so it says
 * exactly which command to run on it rather than failing later with nothing to
 * act on. The command runs on that machine, over SSH, not here.
 */
function HarnessHint({ machineName, command }: { machineName: string; command: string }) {
	const [copied, setCopied] = useState(false);

	return (
		<div
			className="flex items-center gap-2 rounded-md bg-raised px-3 py-2 text-xs leading-row text-settings-muted"
			data-testid="ao-machine-harness-hint"
		>
			<span className="min-w-0 flex-1">
				No agent harness on {machineName}. Run <code className="font-mono text-foreground">{command}</code> on that
				machine.
			</span>
			<Button
				type="button"
				variant="ghost"
				size="sm"
				onClick={async () => {
					await aoBridge.clipboard.writeText(command);
					setCopied(true);
				}}
			>
				{copied ? <Check className="size-icon-sm" aria-hidden="true" /> : <Copy className="size-icon-sm" aria-hidden="true" />}
				{copied ? "Copied" : "Copy"}
			</Button>
		</div>
	);
}
