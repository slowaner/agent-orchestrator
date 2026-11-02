import type { components } from "../../api/schema";

type ReviewReference = {
	id: string;
	prUrl: string;
	githubReviewId: string;
	publication?: components["schemas"]["ReviewPublication"];
};

/** Returns a real provider reference without treating a GitLab object as a GitHub review. */
export function reviewPublicationURL(run: ReviewReference): string | null {
	if (!run.prUrl) return null;
	if (run.prUrl.includes("/-/merge_requests/")) {
		if (run.publication?.provider !== "gitlab" || run.publication.status !== "published") return null;
		const note = run.publication.refs?.find((ref) => ref.kind === "note");
		return note ? `${run.prUrl}#note_${encodeURIComponent(note.id)}` : null;
	}
	return run.githubReviewId ? `${run.prUrl}#pullrequestreview-${run.githubReviewId}` : null;
}

/** Matches original finding notes only; another author's reply remains external. */
export function reviewRunForComment<T extends ReviewReference>(runs: T[], link: { url?: string; reviewId?: string }): T | undefined {
	return runs.find((run) => {
		if (!run.prUrl.includes("/-/merge_requests/")) return !!link.reviewId && run.githubReviewId === link.reviewId;
		if (run.publication?.provider !== "gitlab" || run.publication.status !== "published") return false;
		return run.publication.refs?.some((ref) => ref.kind === "note" && link.url === `${run.prUrl}#note_${encodeURIComponent(ref.id)}`) ?? false;
	});
}
