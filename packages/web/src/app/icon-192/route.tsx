import { ImageResponse } from "next/og";
import { getProjectName } from "@/lib/project-name";
import { renderIconElement } from "@/lib/icon-renderer";

export async function GET() {
  const rawName = getProjectName();
  const name = rawName.replace(/[^\w\s-]/g, "").slice(0, 50) || "AO";
  const response = new ImageResponse(renderIconElement(192, name), {
    width: 192,
    height: 192,
  });
  response.headers.set("Cache-Control", "public, max-age=86400, stale-while-revalidate=3600");
  response.headers.set("Content-Security-Policy", "default-src 'none'; img-src 'self'");
  return response;
}
