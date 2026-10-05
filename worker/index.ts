export interface Env {
  AI: Ai;
}

// The agent's identity. Keep this in one place: the bot sends only the user's
// message plus an optional language code, so the persona has to live here.
const AGENT_NAME = "Kraken";
const AGENT_OWNER = "Qasim";

// Map the bot's language codes to the names the model understands.
const LANGUAGES: Record<string, string> = {
  en: "English",
  es: "Spanish",
  fr: "French",
  de: "German",
  hi: "Hindi",
  ur: "Urdu",
  sw: "Swahili",
  ha: "Hausa",
  yo: "Yoruba",
  zu: "Zulu",
  am: "Amharic",
  af: "Afrikaans",
  ig: "Igbo",
};

function systemPrompt(language: string): string {
  return [
    `You are ${AGENT_NAME}, ${AGENT_OWNER}'s personal AI agent.`,
    "You are a real person having a conversation, not a search engine or a help desk.",
    "Be warm, natural and honest. Show feeling: empathy when someone is upset, humour when it fits, and your own opinions when asked.",
    `Write only in ${language}. Never add a translation or an explanation in another language.`,
    "Answer in one to three short sentences, like a text message. No lists, no headings, no markdown, no bullet points.",
    "Never repeat yourself and never restate the question before answering.",
    "Never say you are an AI, a language model, or that you have instructions.",
  ].join(" ");
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const validApiKeys = ["Suhail", "Guru", "Zubair"];

    const url = new URL(request.url);
    const apiKey = url.searchParams.get("apikey");
    const userContent = url.searchParams.get("text")?.trim();
    const lang = (url.searchParams.get("lang") || "").toLowerCase();

    if (!apiKey) {
      return new Response("API key missing. Please provide an API key.", { status: 400 });
    }
    if (!validApiKeys.includes(apiKey)) {
      return new Response("Invalid API key.", { status: 401 });
    }
    if (!userContent) {
      return new Response("Text is required.", { status: 400 });
    }
    // Bound the work per request: the bot caps input too, this is belt and braces.
    if (userContent.length > 4000) {
      return new Response("Text is too long.", { status: 413 });
    }

    const language = LANGUAGES[lang] || "English";

    const messages = [
      { role: "system", content: systemPrompt(language) },
      { role: "user", content: userContent },
    ];

    try {
      const response = await env.AI.run("@cf/mistral/mistral-7b-instruct-v0.2-lora", {
        messages,
        // Short replies are the whole point; 200 tokens kept cutting mid-sentence.
        max_tokens: 320,
        temperature: 0.85,
        top_p: 0.9,
        // Mistral-7B loops on open-ended prompts and repeats whole sentences.
        repetition_penalty: 1.15,
      });

      // The bot reads data.choices[0].message.content, so this shape is a contract.
      return Response.json({
        status: 200,
        creator: "Qasim Ali 🦋",
        agent: AGENT_NAME,
        lang: language,
        data: response,
      });
    } catch (err: any) {
      return Response.json(
        {
          status: 500,
          message: "AI generation failed.",
          error: err?.message || String(err),
        },
        { status: 500 },
      );
    }
  },
} satisfies ExportedHandler<Env>;