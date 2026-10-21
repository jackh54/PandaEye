import type { ApiError } from "../lib/api";
import type { Session } from "../lib/session";
import { Layout } from "./layout";

export interface ErrorInfo {
  status: number;
  title: string;
  message: string;
}

export function apiErrorInfo(e: ApiError): ErrorInfo {
  switch (e.kind) {
    case "not_found":
      return { status: 404, title: "Not found", message: "The PandaEye API has no record with this id for your account." };
    case "forbidden":
      return { status: 403, title: "Not allowed", message: "Your role does not allow this action." };
    case "unauthorized":
      return {
        status: 503,
        title: "Dashboard not authorised",
        message: "The PandaEye API rejected the dashboard's credentials. An operator needs to check the service token and assertion key.",
      };
    case "misconfigured":
      return { status: 503, title: "Dashboard not configured", message: "The dashboard is missing its PandaEye API configuration." };
    case "client":
      return { status: 400, title: "Request rejected", message: e.message };
    case "timeout":
    case "unreachable":
    case "server":
    case "bad_response":
    default:
      return {
        status: 503,
        title: "PandaEye API unavailable",
        message: "The PandaEye API could not be reached or returned an error. Your servers keep running their local checks; please try again in a minute.",
      };
  }
}

export function ErrorPage(props: { nonce: string; session: Session | null; info: ErrorInfo }) {
  return (
    <Layout title={props.info.title} nonce={props.nonce} session={props.session}>
      <section class="error-page">
        <h1>{props.info.title}</h1>
        <p>{props.info.message}</p>
        <p>
          <a class="btn" href={props.session ? "/" : "/login"}>
            {props.session ? "Back to the overview" : "Go to sign in"}
          </a>
        </p>
      </section>
    </Layout>
  );
}
