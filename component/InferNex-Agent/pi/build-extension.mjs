import { build, version as esbuildVersion } from "esbuild";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const sourceDirectory = dirname(fileURLToPath(import.meta.url));
const projectMetadata = JSON.parse(await readFile(join(sourceDirectory, "package.json"), "utf8"));
if (projectMetadata.devDependencies?.esbuild !== esbuildVersion) {
	throw new Error("installed esbuild does not match the exact package.json pin; run npm ci");
}
const sdkMetadata = JSON.parse(
	await readFile(join(sourceDirectory, "node_modules", "@modelcontextprotocol", "sdk", "package.json"), "utf8"),
);
if (projectMetadata.devDependencies?.["@modelcontextprotocol/sdk"] !== sdkMetadata.version) {
	throw new Error("installed MCP SDK does not match the exact package.json pin; run npm ci");
}
const [outputArgument, licensesArgument] = process.argv.slice(2);
if (!outputArgument || !licensesArgument) {
	throw new Error("usage: node build-extension.mjs OUTPUT.mjs THIRD_PARTY_LICENSES.txt");
}
const output = isAbsolute(outputArgument) ? outputArgument : join(process.cwd(), outputArgument);
const licensesOutput = isAbsolute(licensesArgument) ? licensesArgument : join(process.cwd(), licensesArgument);
await mkdir(dirname(output), { recursive: true });
await mkdir(dirname(licensesOutput), { recursive: true });

const result = await build({
	absWorkingDir: sourceDirectory,
	entryPoints: ["infernex.ts"],
	outfile: output,
	bundle: true,
	platform: "node",
	format: "esm",
	target: "node20",
	banner: { js: 'import { createRequire } from "node:module"; const require = createRequire(import.meta.url);' },
	treeShaking: true,
	legalComments: "none",
	metafile: true,
	logLevel: "warning",
	sourcemap: false,
});

const bundled = await readFile(output, "utf8");
if (bundled.includes(sourceDirectory)) {
	throw new Error("built extension contains an absolute build-machine source path");
}
const loaded = await import(`${pathToFileURL(output).href}?smoke=${Date.now()}`);
if (typeof loaded.default !== "function") {
	throw new Error("built extension does not export its Pi extension entry point");
}

function packageNameForInput(input) {
	const marker = "node_modules/";
	const offset = input.lastIndexOf(marker);
	if (offset < 0) return "";
	const parts = input.slice(offset + marker.length).split("/");
	return parts[0].startsWith("@") ? `${parts[0]}/${parts[1]}` : parts[0];
}

const packageNames = new Set();
for (const input of Object.keys(result.metafile.inputs)) {
	const name = packageNameForInput(input);
	if (name) packageNames.add(name);
}
const notices = [
	"Third-party licenses for the bundled InferNex Pi extension.",
	"Generated from the exact packages used by esbuild; do not edit manually.",
];
for (const name of [...packageNames].sort()) {
	const packageRoot = join(sourceDirectory, "node_modules", ...name.split("/"));
	const metadata = JSON.parse(await readFile(join(packageRoot, "package.json"), "utf8"));
	const licenseFiles = (await readdir(packageRoot))
		.filter((entry) => /^(?:licen[cs]e|copying)(?:\.|$)/i.test(entry))
		.sort();
	if (licenseFiles.length === 0) {
		throw new Error(`bundled package ${name} has no license file`);
	}
	notices.push("", `===== ${name}@${metadata.version} (${metadata.license || "license in package"}) =====`);
	for (const licenseFile of licenseFiles) {
		notices.push("", await readFile(join(packageRoot, licenseFile), "utf8"));
	}
}
await writeFile(licensesOutput, `${notices.join("\n").trimEnd()}\n`, { mode: 0o644 });
