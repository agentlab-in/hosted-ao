import { ImageResponse } from "next/og";
import { getProjectName } from "@/lib/project-name";
import { renderIconElement } from "@/lib/icon-renderer";

export async function GET() {
  const name = getProjectName();
  const response = new ImageResponse(renderIconElement(192, name), {
    width: 192,
    height: 192,
  });
  response.headers.set("Cache-Control", "public, max-age=86400");
  return response;
}
