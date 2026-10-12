import { Trans } from '@lingui/react/macro';
import type { AuthError } from '../auth';

const ErrorCopy = ({ error }: { error: AuthError }) => {
  switch (error.category) {
    case 'unauthorized':
      return error.phase === 'login'
        ? <Trans>The username or password is incorrect.</Trans>
        : <Trans>Your session has ended. Sign in again to continue.</Trans>;
    case 'forbidden':
      return <Trans>This request was denied. Check your session and try again.</Trans>;
    case 'unavailable':
      return <Trans>The Console service is unavailable. Try again in a moment.</Trans>;
    case 'network':
      return <Trans>Could not reach the Console service. Check your connection and try again.</Trans>;
    case 'timeout':
      return <Trans>The Console service took too long to respond. Try again.</Trans>;
    case 'canceled':
      return <Trans>The request was interrupted. Check your session before trying again.</Trans>;
    case 'invalid-response':
      return <Trans>The Console service returned an unexpected response. Check your session and try again.</Trans>;
    case 'invalid-request':
      return <Trans>The request could not be accepted. Check your details and try again.</Trans>;
    case 'not-found':
      return <Trans>The Console service could not find this request.</Trans>;
    case 'conflict':
      return <Trans>Your session changed. Check your session and try again.</Trans>;
    case 'busy':
      return <Trans>Another account action is in progress. Wait for it to finish.</Trans>;
  }
};

export const AuthErrorMessage = ({ error }: { error: AuthError }) => (
  <>
    <span><ErrorCopy error={error} /></span>
    {error.requestId && <span className="request-reference"><Trans>Request ID: {error.requestId}</Trans></span>}
  </>
);
