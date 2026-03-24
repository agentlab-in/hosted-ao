"use client";

import { useEffect, useRef } from "react";
import type { DashboardSession } from "@/lib/types";
import { getSessionTitle } from "@/lib/format";

function getRelativeTime(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  const diffMs = now - then;
  const diffSec = Math.floor(diffMs / 1000);
  if (diffSec < 60) return `${diffSec}s ago`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDay = Math.floor(diffHr / 24);
  return `${diffDay}d ago`;
}

interface BottomSheetProps {
  session: DashboardSession | null;
  onConfirm: () => void;
  onCancel: () => void;
}

export function BottomSheet({ session, onCancel, onConfirm }: BottomSheetProps) {
  const touchStartYRef = useRef<number | null>(null);
  const sheetRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!session) return;

    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onCancel();
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [session, onCancel]);

  function handleTouchStart(e: React.TouchEvent) {
    touchStartYRef.current = e.touches[0]?.clientY ?? null;
  }

  function handleTouchEnd(e: React.TouchEvent) {
    if (touchStartYRef.current === null) return;
    const deltaY = (e.changedTouches[0]?.clientY ?? 0) - touchStartYRef.current;
    touchStartYRef.current = null;
    if (deltaY > 80) {
      onCancel();
    }
  }

  if (!session) return null;

  const title = getSessionTitle(session);
  const branch = session.branch ?? "—";
  const runtime = getRelativeTime(session.createdAt);

  return (
    <>
      {/* Backdrop */}
      <div
        className="bottom-sheet-backdrop"
        onClick={onCancel}
        aria-hidden="true"
      />

      {/* Sheet */}
      <div
        ref={sheetRef}
        className="bottom-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="bottom-sheet-title"
        onTouchStart={handleTouchStart}
        onTouchEnd={handleTouchEnd}
      >
        {/* Drag handle */}
        <div className="bottom-sheet__handle" aria-hidden="true" />

        <div className="bottom-sheet__header">
          <h2 id="bottom-sheet-title" className="bottom-sheet__title">
            Terminate session?
          </h2>
          <p className="bottom-sheet__subtitle">This action cannot be undone.</p>
        </div>

        <div className="bottom-sheet__session-info">
          <div className="bottom-sheet__session-name">{title}</div>
          <div className="bottom-sheet__session-meta">
            {branch !== "—" && (
              <span className="bottom-sheet__session-branch">{branch}</span>
            )}
            <span className="bottom-sheet__session-runtime">Started {runtime}</span>
          </div>
        </div>

        <div className="bottom-sheet__actions">
          <button
            type="button"
            className="bottom-sheet__btn bottom-sheet__btn--cancel"
            onClick={onCancel}
          >
            Cancel
          </button>
          <button
            type="button"
            className="bottom-sheet__btn bottom-sheet__btn--danger"
            onClick={onConfirm}
          >
            Terminate
          </button>
        </div>
      </div>
    </>
  );
}
