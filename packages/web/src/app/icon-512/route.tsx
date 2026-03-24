import { ImageResponse } from "next/og";
import { getProjectName } from "@/lib/project-name";
import { renderIconElement } from "@/lib/icon-renderer";

export async function GET() {
  const name = getProjectName();
  const response = new ImageResponse(renderIconElement(512, name), {
    width: 512,
    height: 512,
  });
  response.headers.set("Cache-Control", "public, max-age=86400");
  return response;
}
