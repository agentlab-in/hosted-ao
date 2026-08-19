// Hosted AO pins upstream AO Cloud off permanently; machines are this fork's
// remote story. See docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md.
//
// Single source of truth for the pin. Both sides of the process boundary
// import this same constant instead of keeping their own copy of the
// boolean:
//   - the renderer (lib/cloud-session.ts) reads it to hide the Cloud
//     sign-in UI;
//   - the main process (main/cloud-auth.ts and main.ts) reads it to skip
//     claiming the ao-app:// protocol and to drop any ao-app:// deep link
//     before it reaches the WorkOS callback handler.
// Flipping only one side back on (e.g. the renderer without the main
// process) would leave a live OS protocol claim with a hidden UI, so the
// gates must always resolve to this one export.
export const CLOUD_SIGN_IN_ENABLED = false;
