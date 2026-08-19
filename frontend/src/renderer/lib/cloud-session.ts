import { useEffect, useState } from "react";
import { aoBridge } from "./bridge";
import type { CloudAccount } from "../../shared/cloud-account";

export type { CloudAccount };

export type CloudSessionStatus = "loading" | "authenticated" | "unauthenticated";

export interface UseCloudSessionResult {
  configured: boolean;
  session: CloudAccount | null;
  status: CloudSessionStatus;
  signIn: (returnTo?: string) => void;
  signOut: () => Promise<void>;
}

// Hosted AO pins upstream AO Cloud off permanently; machines are this fork's
// remote story. See docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md.
const CLOUD_SIGN_IN_ENABLED = false;

export function isCloudSignInConfigured(
  clientId = import.meta.env.VITE_WORKOS_CLIENT_ID,
): boolean {
  return CLOUD_SIGN_IN_ENABLED && Boolean(clientId?.trim());
}

export function useCloudSession(): UseCloudSessionResult {
  const configured = isCloudSignInConfigured();
  const [session, setSession] = useState<CloudAccount | null>(null);
  const [status, setStatus] = useState<CloudSessionStatus>("loading");

  useEffect(() => {
    let active = true;
    aoBridge.cloud.getSession().then((s) => {
      if (!active) return;
      setSession(s);
      setStatus(s ? "authenticated" : "unauthenticated");
    }).catch(() => {
      if (!active) return;
      setSession(null);
      setStatus("unauthenticated");
    });

    const unsub = aoBridge.cloud.onSessionChanged((s) => {
      setSession(s);
      setStatus(s ? "authenticated" : "unauthenticated");
    });

    return () => {
      active = false;
      unsub();
    };
  }, []);

  const signIn = () => {
    void aoBridge.cloud.signIn();
  };

  const signOut = async () => {
    await aoBridge.cloud.signOut();
    setSession(null);
    setStatus("unauthenticated");
  };

  return { configured, session, status, signIn, signOut };
}
