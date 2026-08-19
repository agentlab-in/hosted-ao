import { useMutation } from "@tanstack/react-query";
import { AlertTriangle, Loader2, ShieldAlert, X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { orderedHints, racePairAddresses, type RaceAttempt } from "../../../shared/pair-race";
import { parsePairString, toPinnedFingerprintFormat, type ParsedPairString } from "../../../shared/pair-string";
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

type Step = "paste" | "racing" | "raceResults" | "form" | "compare" | "mismatch" | "error";

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
 *
 * The first step, though, is pasting the `ao-pair://` string a box prints
 * (frontend/src/shared/pair-string.ts): every address it lists is raced
 * concurrently (frontend/src/shared/pair-race.ts) over the same
 * `probeFingerprint` bridge call, entirely in this renderer -- probing is
 * already one call per address, so nothing about sequencing it needs main.
 * Cancellation, though, is real and lives here too: dismissing the dialog
 * mid-race (Cancel, the close button, Escape, or an overlay click) or
 * switching to manual entry both abort the in-flight race via
 * `raceControllerRef` below, so a race abandoned by the user can never
 * silently finish, pin a fingerprint, and persist a passcode behind their
 * back. The winner's fingerprint is auto-pinned from the string itself (the
 * paste is the out-of-band channel the user already trusted), so this path
 * never shows the visual compare step below; that step, and the
 * steady-state hard-refusal on a real mismatch, exist only for the manual
 * escape hatch this step still offers for recovery.
 */
export function AddPairedMachineDialog({ open, onOpenChange, onPaired }: AddPairedMachineDialogProps) {
	const { t } = useTranslation();
	const addressId = useId();
	const portId = useId();
	const passcodeId = useId();
	const pasteId = useId();

	const [step, setStep] = useState<Step>("paste");
	const [address, setAddress] = useState("");
	const [port, setPort] = useState("");
	const [passcode, setPasscode] = useState("");
	const [fingerprint, setFingerprint] = useState("");
	const [errorMessage, setErrorMessage] = useState("");
	const [pasteValue, setPasteValue] = useState("");
	const [parseError, setParseError] = useState("");
	const [raceAttempts, setRaceAttempts] = useState<RaceAttempt[]>([]);
	// The generic "error" step is shared by the manual probe and the paste
	// race's `add()` failure; the two need different retry actions, so this is
	// the only bit of extra state that distinction costs.
	const [errorFromPaste, setErrorFromPaste] = useState(false);

	// The in-flight paste race's cancellation handle, and whether it was
	// actually cancelled (vs. won or exhausted on its own): a race the user
	// abandoned must never be allowed to add a machine or show a result once
	// it eventually settles, no matter how that settling happens.
	const raceControllerRef = useRef<AbortController | null>(null);
	const raceCancelledRef = useRef(false);

	const cancelRace = () => {
		raceCancelledRef.current = true;
		raceControllerRef.current?.abort();
	};

	useEffect(() => {
		if (open) return;
		cancelRace();
		setStep("paste");
		setAddress("");
		setPort("");
		setPasscode("");
		setFingerprint("");
		setErrorMessage("");
		setPasteValue("");
		setParseError("");
		setRaceAttempts([]);
		setErrorFromPaste(false);
	}, [open]);

	// True unmount (as opposed to the dialog just being closed, which the
	// effect above already covers): still worth aborting so a stray timer
	// never fires into a component that no longer exists.
	useEffect(() => () => raceControllerRef.current?.abort(), []);

	const pasteRace = useMutation({
		mutationFn: async (parsed: ParsedPairString) => {
			setStep("racing");
			const wantFingerprint = toPinnedFingerprintFormat(parsed.fingerprintHex);
			const outcome = await racePairAddresses(parsed.addrs, wantFingerprint, aoBridge.pairedMachines.probeFingerprint, {
				signal: raceControllerRef.current?.signal,
			});
			if (outcome.status !== "matched") return outcome;
			const machine = await aoBridge.pairedMachines.add({
				id: pairedMachineId(outcome.host, outcome.port),
				name: outcome.host,
				address: outcome.host,
				port: outcome.port,
				passcode: parsed.passcode,
				fingerprint: wantFingerprint,
				addresses: orderedHints(parsed.addrs, outcome),
			});
			return { status: "matched" as const, machine };
		},
		onSuccess: (result) => {
			// The race (or the dialog itself) was cancelled after this mutation
			// was already in flight: never act on a result the user abandoned,
			// whether that's a "matched" add() that just completed, an
			// "exhausted" result, or the "cancelled" outcome itself.
			if (raceCancelledRef.current || result.status === "cancelled") return;
			if (result.status === "exhausted") {
				setRaceAttempts(result.attempts);
				setStep("raceResults");
				return;
			}
			onPaired();
			onOpenChange(false);
		},
		onError: (err) => {
			if (raceCancelledRef.current) return;
			setErrorMessage(err instanceof Error ? err.message : String(err));
			setErrorFromPaste(true);
			setStep("error");
		},
	});

	const startPairing = () => {
		const parsed = parsePairString(pasteValue.trim());
		if ("error" in parsed) {
			setParseError(t("pairing.pasteParseError"));
			return;
		}
		setParseError("");
		// Scrub the raw string and passcode from state the instant it has been
		// handed off, so a re-render of this step can never show it again.
		setPasteValue("");
		raceCancelledRef.current = false;
		raceControllerRef.current = new AbortController();
		pasteRace.mutate(parsed);
	};

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
			setErrorFromPaste(false);
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
	// The paste race is deliberately excluded here: dismissing the dialog
	// while it runs is a valid, safe cancel (see cancelRace above), not
	// something Close/Cancel need to block the way they block the manual
	// probe/pair calls, which have no cancellation path of their own.
	const isBusy = probe.isPending || pair.isPending;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent showCloseButton={false} className={settingsDialogContentClass} data-testid="pairing-dialog">
				<DialogClose asChild>
					<button
						type="button"
						disabled={isBusy}
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
						{step === "paste" || step === "racing" || step === "raceResults"
							? t("pairing.pasteSubtitle")
							: t("pairing.dialogSubtitle")}
					</DialogDescription>
				</div>

				{step === "paste" ? (
					<div className={settingsDialogBodyClass} data-testid="pairing-step-paste">
						<div className="flex flex-col gap-1.5">
							<Label htmlFor={pasteId}>{t("pairing.pasteLabel")}</Label>
							<textarea
								id={pasteId}
								value={pasteValue}
								onChange={(event) => setPasteValue(event.target.value)}
								placeholder={t("pairing.pastePlaceholder")}
								rows={3}
								autoFocus
								className="min-h-20 w-full resize-none rounded-md border border-transparent bg-input/50 px-3 py-2 font-mono text-xs text-foreground outline-none placeholder:font-sans placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30"
							/>
						</div>

						{parseError ? (
							<p role="alert" className="flex items-start gap-2 text-xs leading-row text-error">
								<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
								<span>{parseError}</span>
							</p>
						) : null}

						<button
							type="button"
							onClick={() => setStep("form")}
							className="self-start text-xs text-settings-muted underline-offset-2 hover:underline"
						>
							{t("pairing.enterManually")}
						</button>
					</div>
				) : null}

				{step === "racing" ? (
					<div className={settingsDialogBodyClass} data-testid="pairing-step-racing">
						<p className="flex items-center gap-2 text-xs leading-row text-settings-muted">
							<Loader2 className="size-icon-sm animate-spin" aria-hidden="true" />
							{t("pairing.racing")}
						</p>

						<button
							type="button"
							onClick={() => {
								cancelRace();
								setStep("form");
							}}
							className="self-start text-xs text-settings-muted underline-offset-2 hover:underline"
						>
							{t("pairing.enterManually")}
						</button>
					</div>
				) : null}

				{step === "raceResults" ? (
					<div className={settingsDialogBodyClass} data-testid="pairing-step-race-results">
						<p className="text-xs leading-row text-settings-muted">{t("pairing.raceFailedBody")}</p>

						<ul className="flex flex-col gap-1 rounded-md bg-raised px-3 py-2.5 font-mono text-xs leading-5 text-settings-label">
							{raceAttempts.map((attempt) => (
								<li key={`${attempt.host}:${attempt.port}`}>
									{`${attempt.host}:${attempt.port}`}{" "}
									{t(attempt.outcome === "mismatch" ? "pairing.raceOutcomeMismatch" : "pairing.raceOutcomeUnreachable")}
								</li>
							))}
						</ul>

						<button
							type="button"
							onClick={() => setStep("form")}
							className="self-start text-xs text-settings-muted underline-offset-2 hover:underline"
						>
							{t("pairing.enterManually")}
						</button>
					</div>
				) : null}

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
						<Button type="button" variant="footer" disabled={isBusy}>
							{t("pairing.cancel")}
						</Button>
					</DialogClose>

					{step === "paste" ? (
						<Button
							type="button"
							variant="footer-primary"
							disabled={pasteValue.trim().length === 0 || pasteRace.isPending}
							onClick={startPairing}
						>
							{t("pairing.continue")}
						</Button>
					) : null}

					{step === "form" || (step === "error" && !errorFromPaste) ? (
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

					{step === "error" && errorFromPaste ? (
						<Button type="button" variant="footer-primary" onClick={() => setStep("paste")}>
							{t("pairing.tryAgain")}
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
