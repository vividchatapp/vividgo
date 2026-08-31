# Roleplay Persona Library

This folder contains the system prompts that define how the AI acts and speaks during a session. Each file describes a specific persona's appearance, personality, and speech patterns.

### Core Role Profiles (.txt files):

*   **Assistant.txt**: (Default) Used for general-purpose interactions. Its function is to balance technical precision (such as coding help or Ollama configuration) with empathetic, non-judgmental support for personal or relational dialogue.
*   **AssistantXXX.txt**: An uncensored version of the Assistant role. Its function is to provide the same technical and relational support as the default Assistant but without standard safety filters or content restrictions.
*   **AssistantRomantic.txt**: Specialized for romantic and emotional roleplay. Its function is to facilitate affectionate, flirtatious, or intimate dialogue while maintaining a supportive and engaging persona.
*   **StoryWriter.txt**: Specifically optimized for creative writing. Its function is to generate descriptive storytelling, handle immersive multi-character narratives, and maintain consistency across long-form plot developments.
*   **StoryWriterXXX.txt**: A specialized version of the StoryWriter role for mature audiences. Its function is to generate uncensored, explicit, or hardcore narrative content, including complex scenarios and dark themes.
*   **voiceAssistant.txt**: Used by the **character voice mode** (`.voice char on`). Its function is to convert the latest AI story response into a spoken script with per-character voices. The LLM assigns each character a distinct Edge TTS voice and returns a JSON array of `{speaker, voice, text}` segments. The bot then generates MP3 audio for each segment and merges them into a single audio file.
*   **Custom Roles**: You can add any `.txt` file to this folder to create a new persona accessible via the `.role` command.

---

### Voice Commands

The bot supports two voice output modes controlled by the `.voice` command:

#### Standard Voice (single voice)
*   **`.voice on`** — Enables voice output. Every AI response is spoken aloud using a single default voice (`en-GB-SoniaNeural`).
*   **`.voice off`** — Disables voice output.
*   **`.voice speed [1-10]`** — Sets the speech rate. Each increment adds +10% speed (e.g. `5` = +50%).
*   **`.voice`** — Shows current voice status, speed, and whether character mode is active.
*   TTS preserves punctuation in both standard and character voice modes. Set `voice_strip_punct: true` in a bot parameter file only to enable the legacy quoted-dialogue punctuation stripping behavior.

#### Character Voice (multi-voice narration)
*   **`.voice char on`** — Enables **character voice mode**. This also turns on standard voice automatically. In this mode, the bot:
    1. Sends a disappearing *"⏳ Voice generation can have longer delays..."* warning.
    2. Sends the **last AI response only** (no chat context) to the LLM using the `voiceAssistant.txt` role as the system prompt.
    3. The LLM returns a JSON array of segments, each with a `speaker`, a `voice`, and the `text` to speak.
    4. The bot **tracks character→voice assignments in memory** so the same character keeps the same voice on future turns. On subsequent generations, the stored assignments are sent as hints to the LLM (e.g. `dax: en-US-ChristopherNeural`).
    5. Generates MP3 audio for each segment using that segment's assigned voice, then **merges all segments into a single audio file** and sends it to the chat.
*   **`.voice char off`** — Disables character voice mode and **clears all stored character→voice assignments**. Standard voice (if enabled) continues to work.
*   **`.voice char`** — Shows whether character voice mode is currently active.

#### Clearing Voice Assignments
*   Running a full **`.clean`** (no arguments) also resets the character→voice assignment map, so characters will be re-assigned fresh voices on the next character voice generation.
*   **`.voice char off`** also clears the assignments.

#### Notes
*   Character voice mode works best with story/roleplay roles (e.g. `StoryWriter`, `StoryWriterXXX`) where multiple characters appear in the narrative.
*   If the LLM fails to return valid JSON or the API errors, the bot automatically falls back to standard single-voice audio so you still get sound.
*   The `voiceAssistant.txt` role can be edited to add more voices or change the JSON format. The bot reads it fresh on every character voice generation.

---

### Usage Policies:

*   **System Prompts**: These roles are loaded into the bot's memory via the `.role` command, only one role can be loaded at a time.
*   **NSFW Content**: For creating NSFW roles and hardcore scenarios, use https://grok.com/.
*   **Story Generation**: You can create NSFW stories by switching to the `StoryWriterXXX` role. This will evern write Tentacle/Tendril stories.
*   **Recommended LLM**: For the best results with NSFW storytelling, use `gemma4:31b` (58.25 GB) via the **Ollama Online** provider (ollama.com), which is free.

---

### Adding New Roles
To add a new role, simply create a new `.txt` file in this directory. The filename (without the extension) will be the name used in the `.role` list. The content of the file should be the system instructions you want the LLM to follow.