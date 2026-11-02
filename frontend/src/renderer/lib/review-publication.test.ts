import { describe, expect, it } from "vitest";
import { reviewPublicationURL, reviewRunForComment } from "./review-publication";

const gitlab = {
	id: "gl", prUrl: "https://gitlab.example/g/sub/r/-/merge_requests/39", githubReviewId: "",
	publication: { provider: "gitlab" as const, status: "published" as const, prUrl: "https://gitlab.example/g/sub/r/-/merge_requests/39", targetSha: "head", refs: [{ kind: "note" as const, id: "101" }, { kind: "discussion" as const, id: "abc" }] },
};
const github = { id: "gh", prUrl: "https://github.com/o/r/pull/1", githubReviewId: "101" };

describe("provider review references", () => {
	it("preserves legacy GitHub links and uses real GitLab note links", () => {
		expect(reviewPublicationURL(github)).toBe(`${github.prUrl}#pullrequestreview-101`);
		expect(reviewPublicationURL(gitlab)).toBe(`${gitlab.prUrl}#note_101`);
		expect(reviewPublicationURL({ ...gitlab, publication: undefined, githubReviewId: "old-invalid-id" })).toBeNull();
	});
	it.each(["blocked", "unconfirmed"] as const)("does not link %s publication as success", (status) => {
		expect(reviewPublicationURL({ ...gitlab, publication: { ...gitlab.publication, status } })).toBeNull();
	});
	it("isolates mixed queues, another MR's identical ID and other replies", () => {
		const runs = [github, gitlab];
		expect(reviewRunForComment(runs, { reviewId: "101" })).toBe(github);
		expect(reviewRunForComment(runs, { url: `${gitlab.prUrl}#note_101` })).toBe(gitlab);
		expect(reviewRunForComment(runs, { url: `${gitlab.prUrl}#note_102` })).toBeUndefined();
		expect(reviewRunForComment(runs, { url: "https://gitlab.example/other/r/-/merge_requests/39#note_101" })).toBeUndefined();
	});
});
