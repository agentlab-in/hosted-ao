import { readFile } from "node:fs/promises";
import { builtinModules } from "node:module";
import typescript from "@rollup/plugin-typescript";

const externalPackages = new Set(["yaml", "zod"]);
const nodeBuiltins = new Set([
  ...builtinModules,
  ...builtinModules.map((moduleName) => `node:${moduleName}`),
]);

function getPackageName(importId) {
  if (importId.startsWith(".") || importId.startsWith("/")) {
    return null;
  }

  const parts = importId.split("/");
  return importId.startsWith("@") ? parts.slice(0, 2).join("/") : parts[0];
}

function rawMarkdown() {
  return {
    name: "raw-markdown",
    async load(id) {
      if (!id.endsWith(".md")) {
        return null;
      }

      return `export default ${JSON.stringify(await readFile(id, "utf8"))};`;
    },
  };
}

export default {
  input: "src/index.ts",
  output: {
    dir: "dist",
    format: "es",
    preserveModules: true,
    preserveModulesRoot: "src",
    sourcemap: true,
  },
  external(id) {
    if (nodeBuiltins.has(id) || id.startsWith("node:")) {
      return true;
    }

    const packageName = getPackageName(id);
    return packageName ? externalPackages.has(packageName) : false;
  },
  plugins: [
    rawMarkdown(),
    typescript({
      compilerOptions: {
        declaration: false,
        declarationMap: false,
        module: "Node16",
      },
      tsconfig: "./tsconfig.json",
    }),
  ],
};
