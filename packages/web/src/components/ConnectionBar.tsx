"use client";

interface ConnectionBarProps {
  status: "connected" | "reconnecting" | "disconnected";
}

export function ConnectionBar({ status }: ConnectionBarProps) {
  const isDisconnected = status === "disconnected";

  return (
    <div
      className={`connection-bar connection-bar--${status}`}
      aria-live={isDisconnected ? "assertive" : "polite"}
      aria-atomic="true"
      onClick={isDisconnected ? () => window.location.reload() : undefined}
      role={isDisconnected ? "button" : undefined}
    >
      {isDisconnected && "Offline · tap to retry"}
    </div>
  );
}
