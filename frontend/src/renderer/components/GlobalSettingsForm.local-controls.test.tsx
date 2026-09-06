import { afterEach, expect, it, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { setApiBaseUrl } from "../lib/api-client";
import { GlobalSettingsForm } from "./GlobalSettingsForm";
vi.mock("./settings/CodexAccountsSection", () => ({ CodexAccountsSection: () => <div>Credential controls</div> }));
vi.mock("./settings/HarnessSettingsSection", () => ({ HarnessSettingsSection: () => <div>Installer controls</div> }));
afterEach(() => { act(() => setApiBaseUrl("http://127.0.0.1:3001")); });
it.each(["agents", "harness"] as const)("requires the local machine for %s, including after selection changes", (section) => {
  setApiBaseUrl("http://127.0.0.1:3001");
  render(<GlobalSettingsForm section={section} />);
  expect(screen.getByText(/controls/)).toBeInTheDocument();
  act(() => setApiBaseUrl("https://machine.example.com"));
  expect(screen.getByRole("status")).toHaveTextContent("Select This computer");
  expect(screen.queryByText(/Credential controls|Installer controls/)).toBeNull();
});
