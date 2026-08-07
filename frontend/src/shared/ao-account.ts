/** The AO account the desktop install is signed in as. Identity only, no credentials. */
export type AoAccount = {
	id: string;
	email: string;
};

export type AoAccountStatus =
	// No stored sign-in. Local mode works exactly the same in this state.
	| "signed-out"
	// A system-browser login is in flight in this app instance.
	| "signing-in"
	| "signed-in"
	// Sign-in cannot be attempted at all: no OS credential store to encrypt the
	// refresh token with, or an unusable AO_CONTROL_URL. `error` says which.
	| "unavailable";

export type AoAccountState = {
	status: AoAccountStatus;
	/** Which control plane this app trusts, so a dev hatch is never invisible. */
	controlPlaneUrl: string;
	/** Present when status is "signed-in". */
	account?: AoAccount;
	/** Last failure, or the reason status is "unavailable". Never contains a token. */
	error?: string;
};
