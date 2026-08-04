# Roleplay Persona Library

This folder contains the system prompts that define how the AI acts and speaks during a session. Each file describes a specific persona's appearance, personality, and speech patterns.

### Core Role Profiles (.txt files):

### Core Role Profiles (.txt files):

*   **Assistant.txt**: (Default) Used for general-purpose interactions. Its function is to balance technical precision (such as coding help or Ollama configuration) with empathetic, non-judgmental support for personal or relational dialogue.
*   **AssistantXXX.txt**: An uncensored version of the Assistant role. Its function is to provide the same technical and relational support as the default Assistant but without standard safety filters or content restrictions.
*   **AssistantRomantic.txt**: Specialized for romantic and emotional roleplay. Its function is to facilitate affectionate, flirtatious, or intimate dialogue while maintaining a supportive and engaging persona.
*   **StoryWriter.txt**: Specifically optimized for creative writing. Its function is to generate descriptive storytelling, handle immersive multi-character narratives, and maintain consistency across long-form plot developments.
*   **StoryWriterXXX.txt**: A specialized version of the StoryWriter role for mature audiences. Its function is to generate uncensored, explicit, or hardcore narrative content, including complex scenarios and dark themes.
*   **Custom Roles**: You can add any `.txt` file to this folder to create a new persona accessible via the `.role` command.
---


---

### Usage Policies:

*   **System Prompts**: These roles are loaded into the bot's memory via the `.role` command, only one role can be loaded at a time.
*   **NSFW Content**: For creating NSFW roles and hardcore scenarios, use https://grok.com/.
*   **Story Generation**: You can create NSFW stories by switching to the `StoryWriterXXX` role. This will evern write Tentacle/Tendril stories.
*   **Recommended LLM**: For the best results with NSFW storytelling, use `gemma4:31b` (58.25 GB) via the **Ollama Online** provider (ollama.com), which is free.

---

### Adding New Roles
To add a new role, simply create a new `.txt` file in this directory. The filename (without the extension) will be the name used in the `.role` list. The content of the file should be the system instructions you want the LLM to follow.