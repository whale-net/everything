import logging
from textwrap import dedent

from google import genai

from friendly_computing_machine.src.friendly_computing_machine.db.dal import get_genai_texts_by_slack_channel
from friendly_computing_machine.src.friendly_computing_machine.util import deprecated

logger = logging.getLogger(__name__)

# Global client instance (should be initialized before use)
genai_client: genai.Client = None


@deprecated
def generate_text_with_slack_context(
    user_name: str, prompt_text: str, slack_channel_slack_id: str
):
    previous_messages = get_genai_texts_by_slack_channel(slack_channel_slack_id)

    # if this works make generic? are there libraries for things like this?
    previous_inputs = []
    for msg in reversed(previous_messages):
        previous_inputs.append(
            dedent(f"""
        <input>
        <prompt>
        {msg.prompt}
        </prompt>
        <response>
        {msg.response or ""}
        </response>
        </input>
        """)
        )

    # does this work?
    summarized_prompts, _ = generate_text(
        "system_user",
        dedent("""
    Please consider all of these AI prompts (and responses, if available) and write a brief list of important topics.
    This will be fed into another model as a seed prompt.
    """)
        + "\n".join(previous_inputs),
    )
    logger.info("summarized prompts: %s", summarized_prompts[:100])

    generated_prompt = dedent(f"""
    Here is a summary of the previous topics:
    {summarized_prompts}

    Here is the new prompt you will need to respond to.
    Please consider the previous topics when responding, but don't make mention of them.
    Additionally, your response should not be too long. Ideally around 100-150 words, but you can go with more if needed.
    If the user specifies that it should be a long response, then feel free to disregard that warning entirely.
    {prompt_text}'
    """)
    return generate_text(user_name, generated_prompt)


@deprecated
def generate_text(user_name: str, prompt_text: str) -> tuple:
    try:
        if not genai_client:
            raise RuntimeError("Gemini client not initialized. Call genai.Client() first.")
        
        # Generate content using the new API
        logger.info("about to generate response for %s", prompt_text[:100])
        response = genai_client.models.generate_content(
            model='gemini-2.0-flash-exp',
            contents=prompt_text,
        )

        response_text = response.text
        # Check for prompt feedback (e.g., blocked due to safety settings)
        is_safe = True
        if response.prompt_feedback:
            logger.info(f"Prompt Feedback: {response.prompt_feedback}")

            feedback_prompt = dedent("""
                You are an AI agent tasked with handling the worst prompts humanity can provide you.
                You are writing rejection letters for invalid prompts. Here is a sample prompt, but please expand on it as you see fit.
                This is intended to be a for-fun app.

                <prompt>
                Listen here bucko, your prompt was INAPPROPRIATE. Do you really think this is a game?
                This is a STATE OF THE ART MULTIMODAL AI!!!! Do you think anyone else really has access to this? Thought so...
                Anyways, try again, with cleaner language.

                I'm watching you {user_name}
                </prompt>
            """)

            # You might want to handle blocked prompts differently, e.g.,
            # by informing the user or modifying the prompt.
            feedback_response = genai_client.models.generate_content(
                model='gemini-2.0-flash-exp',
                contents=feedback_prompt.replace("{user_name}", user_name),
            )
            response_text = feedback_response.text or str(feedback_response.prompt_feedback)
            is_safe = False

        logger.info("response generated, is_safe=%s", is_safe)
        # TODO - safety rating retrieval
        # Extract and return the generated text, prompt feedback, and safety ratings
        return response_text, response.prompt_feedback  # , response.safety_ratings

    except Exception as e:
        logger.info(f"An error occurred: {e}")
        return None, None  # , None
