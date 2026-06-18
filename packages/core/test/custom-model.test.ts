import { afterEach, describe, expect, test } from "bun:test";
import { addCustomModel, removeCustomModel, listCustomModelIds, getModelSync, bustCatalogCache } from "../src/catalog";

// LOOP_DIR is fixed at module load, so HOME can't isolate this — the helpers
// write to the real ~/.loop/models.json. Track and remove what each test adds.
const added: string[] = [];
const track = (id: string) => (added.push(id), id);

afterEach(() => {
    for (const id of added.splice(0)) removeCustomModel(id);
    bustCatalogCache();
});

describe("custom models (~/.loop/models.json overrides)", () => {
    test("add registers a usable model with sane defaults", () => {
        const id = track(addCustomModel({ provider: "openrouter", modelId: "pitest/defaults-x" }));
        expect(id).toBe("openrouter/pitest/defaults-x");
        expect(listCustomModelIds()).toContain(id);
        bustCatalogCache();
        const m = getModelSync(id);
        expect(m?.provider).toBe("openrouter");
        expect(m?.available).toBe(true);
        expect(m?.contextWindow).toBeGreaterThan(0);
    });

    test("explicit fields are honored", () => {
        const id = track(
            addCustomModel({
                provider: "openai",
                modelId: "pitest-gpt-custom",
                name: "PiTest GPT",
                contextWindow: 200_000,
                inputCost: 1.5,
                outputCost: 6,
            }),
        );
        bustCatalogCache();
        const m = getModelSync(id);
        expect(m?.name).toBe("PiTest GPT");
        expect(m?.contextWindow).toBe(200_000);
        expect(m?.cost.input).toBe(1.5);
        expect(m?.cost.output).toBe(6);
    });

    test("remove deletes only the targeted id", () => {
        const a = track(addCustomModel({ provider: "xai", modelId: "pitest-m-a" }));
        const b = track(addCustomModel({ provider: "xai", modelId: "pitest-m-b" }));
        expect(removeCustomModel(a)).toBe(true);
        added.splice(added.indexOf(a), 1);
        expect(listCustomModelIds()).not.toContain(a);
        expect(listCustomModelIds()).toContain(b);
        expect(removeCustomModel("xai/pitest-never-existed")).toBe(false);
    });
});
