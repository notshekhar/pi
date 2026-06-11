import type { GenericOAuthCredentials } from "../../types";

export interface OAuthLoginCallbacks {
    onAuth: (info: { url: string; instructions?: string }) => void;
    onPrompt: (prompt: { message: string; placeholder?: string; allowEmpty?: boolean }) => Promise<string>;
    onProgress?: (message: string) => void;
    signal?: AbortSignal;
}

export interface OAuthProviderInterface {
    id: string;
    name: string;
    login(cb: OAuthLoginCallbacks): Promise<GenericOAuthCredentials>;
    refreshToken(creds: GenericOAuthCredentials): Promise<GenericOAuthCredentials>;
    getApiKey(creds: GenericOAuthCredentials): string;
}
