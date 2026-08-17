import { useMutation } from "@tanstack/react-query";
import { AlertTriangle, Loader2, ShieldAlert, X } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { Button } from "../ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";

type AddPairedMachineDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Called after a successful pairing, so the caller can refetch the list. */
	onPaired: () => void;
};

/**
 * Deterministic id for a paired machine, from its address and port.
 *
 * `pairedMachines.add` upserts by id (docs/adr/0003-pair-mode-gateway.md), so
 * deriving the id from address:port rather than minting a random one on every
 * submit is what makes re-entering the same box's address here *be* the
 * re-pair path, with no separate "re-pair" entry point needed: the same probe
 * -> compare -> accept flow runs again, and `getPinnedFingerprint` below is
 * what tells a genuine re-pair from a fingerprint mismatch before anything is
 * offered to accept.
 */
export function pairedMachineId(address: string, port: number): string {
	return `paired:${address.trim().toLowerCase()}:${port}`;
}

/**
 * SHA-256 fingerprint (32 colon-separated hex octets) chunked into rows of 8
 * so it can actually be eyeballed against the box's printout rather than read
 * as one 95-character line.
 */
function fingerprintRows(fingerprint: string): string[] {
	const octets = fingerprint.split(":");
	const rows: string[] = [];
	for (let i = 0; i < octets.length; i += 8) rows.push(octets.slice(i, i + 8).join(":"));
	return rows;
}

type Step = "form" | "compare" | "mismatch" | "error";

function parsePort(raw: string): number | null {
	if (!/^\d+$/.test(raw.trim())) return null;
	const port = Number(raw.trim());
	return port > 0 && port <= 65535 ? port : null;
}

/**
 * Manual add-machine flow for a paired box (docs/adr/0003-pair-mode-gateway.md):
 * address, port, and passcode, then a fingerprint comparison the user must
 * explicitly accept before anything is pinned. Built entirely on the
 * `ao.pairedMachines` bridge (list/probeFingerprint/getPinnedFingerprint/add);
 * no transport or storage logic lives here.
 */
export function AddPairedMachineDialog({ open, onOpenChange, onPaired }: AddPairedMachineDialogProps) {
	const { t } = useTranslation();
	const addressId = useId();
	const portId = useId();
	const passcodeId = useId();

	const [step, setStep] = useState<Step>("form");
	const [address, setAddress] = useState("");
	const [port, setPort] = useState("");
	const [passcode, setPasscode] = useState("");
	const [fingerprint, setFingerprint] = useState("");
	const [errorMessage, setErrorMessage] = useState("");

	useEffect(() => {
		if (open) return;
		setStep("form");
		setAddress("");
		setPort("");
		setPasscode("");
		setFingerprint("");
		setErrorMessage("");
	}, [open]);

	const probe = useMutation({
		mutationFn: async () => {
			const portNumber = parsePort(port);
			if (portNumber === null) throw new Error(`Not a valid port: ${port}`);
			const trimmedAddress = address.trim();
			const result = await aoBridge.pairedMachines.probeFingerprint(trimmedAddress, portNumber);
			if ("error" in result) throw new Error(result.error);
			const id = pairedMachineId(trimmedAddress, portNumber);
			const pinned = await aoBridge.pairedMachines.getPinnedFingerprint(id);
			return { fingerprint: result.fingerprint, mismatch: pinned !== null && pinned !== result.fingerprint };
		},
		onSuccess: ({ fingerprint: presented, mismatch }) => {
			setFingerprint(presented);
			setStep(mismatch ? "mismatch" : "compare");
		},
		onError: (err) => {
			setErrorMessage(err instanceof Error ? err.message : String(err));
			setStep("error");
		},
	});

	const pair = useMutation({
		mutationFn: async () => {
			const portNumber = parsePort(port);
			if (portNumber === null) throw new Error(`Not a valid port: ${port}`);
			const trimmedAddress = address.trim();
			return aoBridge.pairedMachines.add({
				id: pairedMachineId(trimmedAddress, portNumber),
				name: trimmedAddress,
				address: trimmedAddress,
				port: portNumber,
				passcode,
				fingerprint,
			});
		},
		onSuccess: () => {
			onPaired();
			onOpenChange(false);
		},
	});

	const canSubmit = address.trim().length > 0 && parsePort(port) !== null && passcode.length > 0;
	const pairError = pair.error instanceof Error ? pair.error.message : null;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent showCloseButton={false} className={settingsDialogContentClass} data-testid="pairing-dialog">
				<DialogClose asChild>
					<button
						type="button"
						disabled={probe.isPending || pair.isPending}
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("common.close")}
						title={t("common.close")}
					>
						<X className="size-5" aria-hidden="true" />
					</button>
				</DialogClose>

				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{t("pairing.dialogTitle")}</DialogTitle>
					<DialogDescription className="text-control leading-4 text-settings-muted">
						{t("pairing.dialogSubtitle")}
					</DialogDescription>
				</div>

				{step === "form" ? (
					probe.isPending ? (
						<div className={settingsDialogBodyClass} data-testid="pairing-step-probing">
							<p className="flex items-center gap-2 text-xs leading-row text-settings-muted">
								<Loader2 className="size-icon-sm animate-spin" aria-hidden="true" />
								{t("pairing.probing")}
							</p>
						</div>
					) : (
						<div className={settingsDialogBodyClass} data-testid="pairing-step-form">
							<div className="flex flex-col gap-1.5">
								<Label htmlFor={addressId}>{t("pairing.addressLabel")}</Label>
								<Input
									id={addressId}
									value={address}
									onChange={(event) => setAddress(event.target.value)}
									placeholder={t("pairing.addressPlaceholder")}
									autoFocus
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label htmlFor={portId}>{t("pairing.portLabel")}</Label>
								<Input
									id={portId}
									inputMode="numeric"
									value={port}
									onChange={(event) => setPort(event.target.value)}
									placeholder={t("pairing.portPlaceholder")}
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label htmlFor={passcodeId}>{t("pairing.passcodeLabel")}</Label>
								<Input
									id={passcodeId}
									type="password"
									autoComplete="off"
									value={passcode}
									onChange={(event) => setPasscode(event.target.value)}
									placeholder={t("pairing.passcodePlaceholder")}
								/>
							</div>
						</div>
					)
				) : null}

				{step === "error" ? (
					<div className={settingsDialogBodyClass} data-testid="pairing-step-error">
						<p role="alert" className="flex items-start gap-2 text-xs leading-row text-error">
							<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
							<span>{errorMessage}</span>
						</p>
					</div>
				) : null}

				{step === "compare" || step === "mismatch" ? (
					<div className={settingsDialogBodyClass} data-testid={`pairing-step-${step}`}>
						{step === "mismatch" ? (
							<p role="alert" className="flex items-start gap-2 text-xs leading-row text-error">
								<ShieldAlert className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
								<span>{t("pairing.mismatchBody")}</span>
							</p>
						) : (
							<p className="text-xs leading-row text-settings-muted">{t("pairing.compareBody")}</p>
						)}

						<div
							role="group"
							aria-label={t("pairing.fingerprintLabel")}
							className="flex flex-col gap-1 rounded-md bg-raised px-3 py-2.5 font-mono text-xs leading-5 tracking-wide text-settings-label"
							data-testid="pairing-fingerprint"
						>
							{fingerprintRows(fingerprint).map((row, index) => (
								// Index is a fine key here: a static fingerprint's rows never
								// reorder, add, or remove within one probe result.
								<span key={index}>{row}</span>
							))}
						</div>

						{pairError ? (
							<p role="alert" className="text-xs leading-row text-error">
								{pairError}
							</p>
						) : null}
					</div>
				) : null}

				<div className={settingsDialogFooterClass}>
					<DialogClose asChild>
						<Button type="button" variant="footer" disabled={probe.isPending || pair.isPending}>
							{t("pairing.cancel")}
						</Button>
					</DialogClose>

					{step === "form" || step === "error" ? (
						<Button
							type="button"
							variant="footer-primary"
							disabled={!canSubmit || probe.isPending}
							onClick={() => probe.mutate()}
						>
							{probe.isPending ? <Loader2 className="size-icon-sm animate-spin" aria-hidden="true" /> : null}
							{step === "error" ? t("pairing.tryAgain") : t("pairing.continue")}
						</Button>
					) : null}

					{step === "compare" ? (
						<Button
							type="button"
							variant="footer-primary"
							disabled={pair.isPending}
							onClick={() => pair.mutate()}
						>
							{pair.isPending ? <Loader2 className="size-icon-sm animate-spin" aria-hidden="true" /> : null}
							{pair.isPending ? t("pairing.pairing") : t("pairing.accept")}
						</Button>
					) : null}

					{step === "mismatch" ? (
						<Button type="button" variant="footer-primary" onClick={() => setStep("compare")}>
							{t("pairing.rePair")}
						</Button>
					) : null}
				</div>
			</DialogContent>
		</Dialog>
	);
}
