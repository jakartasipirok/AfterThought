import fs from "node:fs/promises";
import path from "node:path";
import { prepareLearningData } from "../frontend/src/learning-data.mjs";

const [original, additional, novel, destination = ".afterthought/learning-demo.json"] = process.argv.slice(2);
if (!original || !additional || !novel) throw new Error("Usage: node scripts/prepare_learning_demo.mjs BASELINE.csv NEW.csv NOVEL.csv [OUTPUT.json]");
const inputs = await Promise.all([original, additional, novel].map(file => fs.readFile(file, "utf8")));
const data = prepareLearningData(...inputs);
await fs.mkdir(path.dirname(destination), { recursive: true });
await fs.writeFile(destination, JSON.stringify(data), { mode: 0o600 });
console.log(JSON.stringify({ baseline: data.baseline.rows, benchmark: data.benchmark.rows, rounds: data.rounds.map(round => round.rows), duplicates: data.duplicates, conflictingFeatureGroups: data.conflicts }));
